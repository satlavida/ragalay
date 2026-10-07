package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/embed/openai"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/sidecar"
	"github.com/satlavida/ragalay/internal/store"
)

// errNotSetUp explains why indexing was skipped.
var errNotSetUp = errors.New(`the AI models are not set up on this computer; run "ragalay setup" to index`)

// indexRuntime is what indexing needs from a profile.
type indexRuntime struct {
	newEmbedder func(ctx context.Context) (index.Embedder, error)
	tokenizer   chunk.Tokenizer
	close       func()
}

// newIndexRuntime prepares indexing for emb. Local profiles need setup; the
// indexing model itself only starts when there is work.
func newIndexRuntime(root string, emb config.Embed) (*indexRuntime, error) {
	prof, err := emb.Lookup()
	if err != nil {
		return nil, err
	}
	if !prof.Local() {
		c, err := newServiceClient(emb)
		if err != nil {
			return nil, err
		}
		return &indexRuntime{
			// The service's tokenizer is unknown: estimate (plan2 §5.7).
			tokenizer:   chunk.Estimate{},
			close:       func() {},
			newEmbedder: func(context.Context) (index.Embedder, error) { return c, nil },
		}, nil
	}
	cache, err := setup.CacheDir()
	if err != nil {
		return nil, err
	}
	st, err := setup.LoadState(cache)
	if err != nil {
		return nil, err
	}
	if !st.Ready(prof.Name) {
		return nil, errNotSetUp
	}
	if err := setup.EnsureSidecar(cache, &st); err != nil {
		return nil, err
	}
	rt := &indexRuntime{tokenizer: chunk.Estimate{}, close: func() {}}
	// The query model's tokenizer counts tokens exactly like the indexing
	// model (shared vocabulary).
	if lq, err := llama.Open(st.LlamaLib, st.Profile(prof.Name).QueryModel, llama.OptionsFor(prof)); err == nil {
		rt.tokenizer, rt.close = lq, func() { lq.Close() }
	}
	logs := filepath.Join(root, config.DirName, config.LogsDir)
	rt.newEmbedder = func(ctx context.Context) (index.Embedder, error) {
		sideLog, err := os.OpenFile(filepath.Join(logs, "sidecar.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		c, err := sidecar.Start(ctx, sidecar.Options{
			Python: st.Python, ScriptPath: st.Sidecar, Model: prof.IndexModel,
			Revision: prof.IndexRevision, MaxSide: emb.ImageMaxSide, Log: sideLog,
		})
		if err != nil {
			sideLog.Close()
			return nil, err
		}
		return &closingEmbedder{Client: c, log: sideLog}, nil
	}
	return rt, nil
}

type closingEmbedder struct {
	*sidecar.Client
	log io.Closer
}

func (c *closingEmbedder) Close() error {
	err := c.Client.Close()
	c.log.Close()
	return err
}

// newQuerier returns emb's query embedder and the name of its query model
// (part of the query cache key). Search never starts Python.
func newQuerier(emb config.Embed) (embed.Querier, string, error) {
	prof, err := emb.Lookup()
	if err != nil {
		return nil, "", err
	}
	if !prof.Local() {
		c, err := newServiceClient(emb)
		id := "openai/" + emb.OpenAI.Model + "@" + emb.OpenAI.Host()
		if err != nil {
			return nil, id, err
		}
		return c, id, nil
	}
	qm, _ := setup.QueryModelFor(prof.Name)
	cache, err := setup.CacheDir()
	if err != nil {
		return nil, qm.ID, err
	}
	st, err := setup.LoadState(cache)
	if err != nil {
		return nil, qm.ID, err
	}
	if !st.QueryReady(prof.Name) {
		return nil, qm.ID, fmt.Errorf("the search model for %s is not installed (run \"ragalay setup\")", prof.Title)
	}
	q, err := llama.Open(st.LlamaLib, st.Profile(prof.Name).QueryModel, llama.OptionsFor(prof))
	if err != nil {
		return nil, qm.ID, err
	}
	return q, qm.ID, nil
}

// newServiceClient builds the OpenAI-compatible client for emb. The API key
// comes from the environment variable the settings name (plan2 P6).
func newServiceClient(emb config.Embed) (*openai.Client, error) {
	o := emb.OpenAI
	if o == nil {
		return nil, errors.New(`embed.openai is missing from .ragalay/config.toml`)
	}
	key := ""
	if o.APIKeyEnv != "" {
		if key = os.Getenv(o.APIKeyEnv); key == "" {
			return nil, fmt.Errorf("the API key environment variable %s is not set (see \"ragalay model show\")", o.APIKeyEnv)
		}
	}
	return openai.New(openai.Options{
		BaseURL: o.BaseURL, Model: o.Model, APIKey: key,
		Dimensions: o.Dimensions, Dim: emb.Dim, Matryoshka: o.Matryoshka,
		ImageInput: o.ImageInput, QueryPrefix: o.QueryPrefix, DocumentPrefix: o.DocumentPrefix,
		QueryExtra: o.QueryExtra, DocumentExtra: o.DocumentExtra,
		BatchSize: o.BatchSize, Concurrency: o.EffectiveConcurrency(), MaxInputTokens: o.MaxInputTokens,
		Timeout: o.Timeout.Duration, QueryTimeout: o.QueryTimeout.Duration, MaxSide: emb.ImageMaxSide,
	}), nil
}

// errNeedConsent stops a first upload to a service nobody agreed to.
type errNeedConsent struct{ host, summary string }

func (e *errNeedConsent) Error() string {
	return fmt.Sprintf("%s would be sent to %s to be embedded. Run \"ragalay scan --yes\" once to allow it", e.summary, e.host)
}

// consentKey records, per folder, that documents may go to a host.
func consentKey(host string) string { return "consent_upload:" + host }

// checkUploadConsent asks once per host per folder before documents leave
// this computer (plan2 S10, S17). Loopback services never ask.
func (a *app) checkUploadConsent(ctx context.Context, root string, emb config.Embed, quiet bool) error {
	if !emb.Remote() {
		return nil
	}
	host := emb.OpenAI.Host()
	var given string
	var docs, images, pdfs int
	var mdBytes, pdfBytes int64
	err := store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var err error
		if given, err = store.Meta(ctx, db, consentKey(host)); err != nil || given != "" {
			return err
		}
		return db.QueryRowContext(ctx, `SELECT count(*),
			coalesce(sum(CASE WHEN d.kind = 'image' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN d.kind = 'pdf' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN d.kind = 'markdown' THEN d.size ELSE 0 END), 0),
			coalesce(sum(CASE WHEN d.kind = 'pdf' THEN d.size ELSE 0 END), 0)
			FROM jobs j JOIN documents d ON d.id = j.document_id WHERE j.state = 'queued'`).
			Scan(&docs, &images, &pdfs, &mdBytes, &pdfBytes)
	})
	if err != nil || given != "" || docs == 0 {
		return err
	}
	// Rough: Markdown ~4 bytes a token, PDF files ~30 bytes per text token.
	tokens := mdBytes/4 + pdfBytes/30
	summary := fmt.Sprintf("%d documents (about %d tokens of text", docs, tokens)
	if emb.OpenAI.ImageInput != config.ImageNone && images > 0 {
		summary += fmt.Sprintf(", %d images", images)
	}
	summary += ")"
	if !a.yes {
		if !a.interactive() || quiet {
			return &errNeedConsent{host: host, summary: summary}
		}
		fmt.Fprintf(a.stdout, "This folder embeds with %s at %s, which is not on this computer.\n%s will be sent there.\n",
			emb.OpenAI.Model, host, summary)
		if strings.HasPrefix(emb.OpenAI.BaseURL, "http://") {
			fmt.Fprintln(a.stdout, "The connection is not encrypted (http).")
		}
		ok, err := a.confirm("Type yes to send them: ", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not sent: indexing needs the documents to go to the embedding service")
		}
	}
	return store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			return store.SetMeta(ctx, tx, consentKey(host), time.Now().UTC().Format(time.RFC3339))
		})
	})
}

// setupReady reports whether emb's profile can index and search on this
// computer.
func setupReady(st setup.State, emb config.Embed) bool { return st.Ready(emb.Profile) }

// searchReady reports whether emb's profile can embed queries.
func searchReady(st setup.State, emb config.Embed) bool {
	if emb.Profile == embed.OpenAI {
		return true
	}
	return st.QueryReady(emb.Profile)
}

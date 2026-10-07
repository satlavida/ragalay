package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/sidecar"
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
		return nil, fmt.Errorf("indexing with %s is not available in this build yet", prof.Title)
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
		return nil, "", fmt.Errorf("searching with %s is not available in this build yet", prof.Title)
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

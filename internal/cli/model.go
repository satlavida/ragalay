package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/store"
)

// ModelOption is one model a folder can use (`model list --json`).
type ModelOption struct {
	Name       string   `json:"name"`
	Title      string   `json:"title"`
	License    string   `json:"license"`
	Pitch      string   `json:"pitch"`
	Modalities []string `json:"modalities"`
	DefaultDim int      `json:"default_dim,omitempty"`
	Local      bool     `json:"local"`     // runs on this computer
	Installed  bool     `json:"installed"` // set up on this computer (always true for a service)
	Active     bool     `json:"active"`    // this folder's settings
}

func modelOptions(emb config.Embed) []ModelOption {
	st := setup.State{}
	if cache, err := setup.CacheDir(); err == nil {
		st, _ = setup.LoadState(cache)
	}
	var out []ModelOption
	for _, p := range embed.Profiles() {
		out = append(out, ModelOption{Name: p.Name, Title: p.Title, License: p.License, Pitch: p.Pitch,
			Modalities: p.Modalities, DefaultDim: p.DefaultDim, Local: p.Local(),
			Installed: st.Ready(p.Name), Active: emb.Profile == p.Name})
	}
	return out
}

// EmbedInfo describes a folder's model (`status --json` "embed", `model show`).
type EmbedInfo struct {
	Profile      string   `json:"profile"`
	Title        string   `json:"title"`
	Model        string   `json:"model"`
	Dim          int      `json:"dim"`
	EmbedID      string   `json:"embed_id"`
	EndpointHost string   `json:"endpoint_host,omitempty"`
	Remote       bool     `json:"remote"` // documents and queries leave this computer
	Modalities   []string `json:"modalities"`
	ImageMode    string   `json:"image_mode,omitempty"`
	APIKeyEnv    string   `json:"api_key_env,omitempty"`
	APIKeySet    bool     `json:"api_key_set,omitempty"`
	License      string   `json:"license"`
}

func embedInfo(emb config.Embed) EmbedInfo {
	p, _ := emb.Lookup()
	in := EmbedInfo{Profile: emb.Profile, Title: p.Title, Model: p.IndexModel, Dim: emb.Dim, EmbedID: emb.SpaceID(),
		Remote: emb.Remote(), Modalities: emb.Modalities(), License: p.License}
	if o := emb.OpenAI; emb.Profile == embed.OpenAI && o != nil {
		in.Model, in.EndpointHost, in.ImageMode, in.APIKeyEnv = o.Model, o.Host(), o.ImageInput, o.APIKeyEnv
		in.APIKeySet = o.APIKeyEnv != "" && os.Getenv(o.APIKeyEnv) != ""
	}
	return in
}

func (a *app) modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Choose the AI model this folder uses",
		Long: `ragalay can use EmbeddingGemma 2 (the default), Jina v5, or any embedding
service that speaks the OpenAI API: Ollama, LM Studio, llama-server, vLLM on
this computer, or an online service.

Changing the model rebuilds the index. Search keeps using the current model
until the rebuild finishes.`,
	}
	cmd.AddCommand(a.modelListCmd(), a.modelShowCmd(), a.modelUseCmd(), a.modelTestCmd())
	return cmd
}

func (a *app) modelListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the models a folder can use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			emb := config.Default().Embed
			if root, err := a.root(); err == nil {
				if cfg, err := config.Load(root); err == nil {
					emb = cfg.Embed
				}
			}
			opts := modelOptions(emb)
			if asJSON {
				return a.printJSON(opts)
			}
			for _, o := range opts {
				mark := "  "
				if o.Active {
					mark = "* "
				}
				state := "not installed"
				if !o.Local {
					state = "service"
				} else if o.Installed {
					state = "installed"
				}
				fmt.Fprintf(a.stdout, "%s%-18s %-36s %-14s %s\n", mark, o.Name, o.Title, state, o.Pitch)
			}
			fmt.Fprintln(a.stdout, "\n* = this folder. Change with: ragalay model use <name>")
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// ModelShow is `model show --json`.
type ModelShow struct {
	EmbedInfo
	IndexEmbedID string        `json:"index_embed_id"` // what search uses now
	Switch       *store.Shadow `json:"model_switch"`
	Mismatch     bool          `json:"mismatch"`
}

func (a *app) modelShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show this folder's model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			sp, err := index.ReadSpaces(cmd.Context(), root, cfg)
			if err != nil {
				return err
			}
			show := ModelShow{EmbedInfo: embedInfo(cfg.Embed), IndexEmbedID: sp.Live, Switch: sp.Shadow, Mismatch: sp.Mismatch()}
			if asJSON {
				return a.printJSON(show)
			}
			w := a.stdout
			fmt.Fprintf(w, "Model:      %s (%s)\n", show.Title, show.Profile)
			if show.EndpointHost != "" {
				fmt.Fprintf(w, "Service:    %s at %s\n", show.Model, show.EndpointHost)
				if show.Remote {
					fmt.Fprintln(w, "            on another computer: your documents and searches are sent there")
				} else {
					fmt.Fprintln(w, "            on this computer")
				}
				if show.APIKeyEnv != "" {
					state := "set"
					if !show.APIKeySet {
						state = "NOT set"
					}
					fmt.Fprintf(w, "API key:    from the environment variable %s (%s)\n", show.APIKeyEnv, state)
					if !show.APIKeySet {
						fmt.Fprint(w, keyHelp(show.APIKeyEnv))
					}
				}
			} else {
				fmt.Fprintf(w, "Runs on:    this computer (%s)\n", show.Model)
			}
			fmt.Fprintf(w, "Dimensions: %d\nEmbeds:     %s\nLicense:    %s\n", show.Dim, strings.Join(show.Modalities, ", "), show.License)
			switch {
			case sp.Shadow != nil:
				fmt.Fprintf(w, "\nSwitching to this model: %d of %d documents done.\n", sp.Shadow.Done, sp.Shadow.Total)
			case sp.Mismatch():
				fmt.Fprintln(w, "\nThe index was built with another model. Run \"ragalay reembed\" to switch.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// keyHelp explains how to set an environment variable, per OS (plan2 S11).
func keyHelp(name string) string {
	if isWindows() {
		return fmt.Sprintf("            Set it (new windows see it):  setx %s \"your-key\"\n"+
			"            or in the ragalay window: Model view, then k\n", name)
	}
	return fmt.Sprintf("            Set it: add  export %s=\"your-key\"  to ~/.zshrc or ~/.bashrc, then open a new terminal\n", name)
}

func (a *app) modelUseCmd() *cobra.Command {
	var dim int
	var device string
	var svc config.OpenAI
	var acceptLicense, noIndex, force bool
	cmd := &cobra.Command{
		Use:   "use <embeddinggemma-2|jina-v5|openai>",
		Short: "Switch this folder to another model and rebuild the index",
		Long: `Switches the folder's model, installs it if needed, and rebuilds the index.
Search keeps using the current model until the rebuild finishes; it is safe to
stop and continues with "ragalay scan".

For a service, give its address and model, e.g.
  ragalay model use openai --base-url http://localhost:11434/v1 --model nomic-embed-text
  ragalay model use openai --base-url https://api.openai.com/v1 --model text-embedding-3-small \
      --api-key-env OPENAI_API_KEY --dim 1536
The API key is read from the environment variable you name; it is never
written to the folder.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			if cfg.Embed, err = applyModel(cmd, cfg.Embed, args[0], dim, svc); err != nil {
				return err
			}
			if cfg.Embed.Profile == embed.OpenAI {
				// Check the service before changing anything; its vector
				// size becomes dim unless one was given.
				fmt.Fprintf(a.stdout, "Checking %s at %s…\n", cfg.Embed.OpenAI.Model, cfg.Embed.OpenAI.Host())
				res, err := probeModel(ctx, cfg.Embed)
				a.printProbe(res)
				if err != nil {
					return fmt.Errorf("the service did not answer as expected, so nothing was changed: %w", err)
				}
				if cfg.Embed.Dim == 0 {
					cfg.Embed.Dim = res.Dim
				}
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := config.Save(root, cfg); err != nil {
				return err
			}
			info := embedInfo(cfg.Embed)
			if info.EndpointHost != "" {
				fmt.Fprintf(a.stdout, "This folder now uses %s at %s (%d dimensions).\n", info.Model, info.EndpointHost, info.Dim)
			} else {
				fmt.Fprintf(a.stdout, "This folder now uses %s (%d dimensions).\n", info.Title, info.Dim)
			}
			if st := loadSetupState(); !st.Ready(cfg.Embed.Profile) {
				if err := a.installModels(ctx, root, cfg, device, acceptLicense, a.yes); err != nil {
					return err
				}
			}
			if noIndex {
				fmt.Fprintln(a.stdout, `Run "ragalay reembed" to rebuild the index.`)
				return nil
			}
			l, err := lockAcquire(root, "model use")
			if err != nil {
				return err
			}
			defer l.Release()
			if _, err := scan.Run(ctx, root, cfg); err != nil {
				return err
			}
			rb, err := index.StartRebuild(ctx, root, cfg, force)
			if err != nil {
				return err
			}
			a.printRebuild(rb)
			sum, err := a.indexQueue(ctx, root, cfg, false)
			if sum != nil && sum.Switched {
				fmt.Fprintln(a.stdout, "Model switch finished: search now uses the new model.")
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	f := cmd.Flags()
	f.IntVar(&dim, "dim", 0, "vector dimensions (default: the model's; a service's size is detected)")
	f.StringVar(&device, "device", "auto", "indexing accelerator for a model installed now")
	f.BoolVar(&acceptLicense, "accept-license", false, "accept a non-commercial model license (Jina v5) without asking")
	f.BoolVar(&a.yes, "yes", false, "download without asking and allow sending documents to the service")
	f.BoolVar(&noIndex, "no-index", false, "only change the settings")
	f.BoolVar(&force, "force", false, "rebuild even if nothing changed, and skip the free-space check")
	f.StringVar(&svc.BaseURL, "base-url", "", "service address ending in /v1")
	f.StringVar(&svc.Model, "model", "", "service model name")
	f.StringVar(&svc.APIKeyEnv, "api-key-env", "", "NAME of the environment variable holding the API key")
	f.StringVar(&svc.ImageInput, "image-input", "", "how the service takes images: none, jina, vllm, llamacpp")
	f.StringVar(&svc.QueryPrefix, "query-prefix", "", `text put before searches, e.g. "search_query: "`)
	f.StringVar(&svc.DocumentPrefix, "document-prefix", "", `text put before documents, e.g. "search_document: "`)
	f.IntVar(&svc.Dimensions, "dimensions", 0, `ask the service for this many dimensions ("dimensions" field)`)
	f.BoolVar(&svc.Matryoshka, "matryoshka", false, "the model allows cutting vectors to --dim")
	return cmd
}

// applyModel changes emb to profile, taking service settings from flags
// that were set. A service's dim is detected later when not given.
func applyModel(cmd *cobra.Command, emb config.Embed, profile string, dim int, svc config.OpenAI) (config.Embed, error) {
	keepSvc := emb.OpenAI
	out, err := emb.UseProfile(profile, dim)
	if err != nil {
		return emb, err
	}
	if profile != embed.OpenAI {
		out.OpenAI = keepSvc // remembered for switching back
		return out, nil
	}
	o := *out.OpenAI
	set := func(name string) bool { return cmd != nil && cmd.Flags().Changed(name) }
	for name, apply := range map[string]func(){
		"base-url": func() { o.BaseURL = svc.BaseURL }, "model": func() { o.Model = svc.Model },
		"api-key-env": func() { o.APIKeyEnv = svc.APIKeyEnv }, "image-input": func() { o.ImageInput = svc.ImageInput },
		"query-prefix": func() { o.QueryPrefix = svc.QueryPrefix }, "document-prefix": func() { o.DocumentPrefix = svc.DocumentPrefix },
		"dimensions": func() { o.Dimensions = svc.Dimensions }, "matryoshka": func() { o.Matryoshka = svc.Matryoshka },
	} {
		if set(name) {
			apply()
		}
	}
	out.OpenAI = &o
	if dim == 0 {
		out.Dim = 0 // detected by the probe
	}
	return out, nil
}

func loadSetupState() setup.State {
	cache, err := setup.CacheDir()
	if err != nil {
		return setup.State{}
	}
	st, _ := setup.LoadState(cache)
	return st
}

func (a *app) modelTestCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Check that this folder's model answers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			res, err := probeModel(cmd.Context(), cfg.Embed)
			if asJSON {
				out := map[string]any{"result": res}
				if err != nil {
					out["error"] = err.Error()
				}
				a.printJSON(out)
			} else {
				a.printProbe(res)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// ProbeResult is what `model test` found.
type ProbeResult struct {
	Profile    string  `json:"profile"`
	Dim        int     `json:"dim"`        // returned by the model
	Normalized bool    `json:"normalized"` // unit length as returned
	DocumentMS int64   `json:"document_ms"`
	QueryMS    int64   `json:"query_ms"`
	Similarity float64 `json:"similarity"` // related query vs document; should be clearly above unrelated
	Unrelated  float64 `json:"unrelated"`
	Image      string  `json:"image"` // "ok", "not used", or the error
}

// probeModel embeds a document, a related and an unrelated query (and an
// image when the service takes images) and checks the answers.
func probeModel(ctx context.Context, emb config.Embed) (ProbeResult, error) {
	res := ProbeResult{Profile: emb.Profile, Image: "not used"}
	q, _, err := newQuerier(emb)
	if err != nil {
		return res, err
	}
	defer q.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t0 := time.Now()
	related, err := q.EmbedQuery(ctx, "how do transformers use attention")
	if err != nil {
		return res, err
	}
	res.QueryMS = time.Since(t0).Milliseconds()
	res.Dim = len(related)
	var n float64
	for _, x := range related {
		n += float64(x) * float64(x)
	}
	res.Normalized = math.Abs(math.Sqrt(n)-1) < 1e-3
	if emb.Profile != embed.OpenAI {
		return res, nil // a local model: setup's self-test checked the indexing side
	}
	unrelated, err := q.EmbedQuery(ctx, "a recipe for sourdough bread")
	if err != nil {
		return res, err
	}
	c, err := newServiceClient(emb)
	if err != nil {
		return res, err
	}
	t0 = time.Now()
	docs, err := c.EmbedDocuments(ctx, []embed.Input{{Modality: embed.Text,
		Text: "The Transformer relies entirely on attention to draw global dependencies between input and output."}})
	if err != nil {
		return res, err
	}
	res.DocumentMS = time.Since(t0).Milliseconds()
	res.Similarity, res.Unrelated = embed.Cosine(related, docs[0]), embed.Cosine(unrelated, docs[0])
	if slices.Contains(emb.Modalities(), embed.Image) {
		res.Image = "ok"
		img, err := probeImage()
		if err == nil {
			defer os.Remove(img)
			_, err = c.EmbedDocuments(ctx, []embed.Input{{Modality: embed.Image, Path: img}})
		}
		if err != nil {
			res.Image = err.Error()
		}
	}
	if res.Similarity <= res.Unrelated {
		return res, fmt.Errorf("the service's vectors do not rank a matching query above an unrelated one (%.3f vs %.3f); check the model and prefixes", res.Similarity, res.Unrelated)
	}
	return res, nil
}

func probeImage() (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 160, 255})
		}
	}
	f, err := os.CreateTemp("", "ragalay-probe-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return f.Name(), png.Encode(f, img)
}

func (a *app) printProbe(r ProbeResult) {
	w := a.stdout
	fmt.Fprintf(w, "Vectors:   %d dimensions", r.Dim)
	if !r.Normalized {
		fmt.Fprint(w, " (not unit length; ragalay normalizes them)")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Search:    %d ms per query\n", r.QueryMS)
	if r.DocumentMS > 0 {
		fmt.Fprintf(w, "Documents: %d ms for one passage\n", r.DocumentMS)
		fmt.Fprintf(w, "Sense:     matching query %.3f, unrelated %.3f\n", r.Similarity, r.Unrelated)
	}
	if r.Image != "not used" {
		fmt.Fprintf(w, "Images:    %s\n", r.Image)
	}
}

// pruneModels lists the models in the shared folder and removes the chosen
// ones (plan2 S15).
func (a *app) pruneModels(remove []string, yes bool) error {
	cache, err := setup.CacheDir()
	if err != nil {
		return err
	}
	list := setup.ListInstalled(cache)
	if len(list) == 0 {
		fmt.Fprintln(a.stdout, "No models are installed in", cache)
		return nil
	}
	w := a.stdout
	if len(remove) == 0 {
		fmt.Fprintf(w, "Models in %s (shared by every ragalay folder on this computer):\n", cache)
		for i, in := range list {
			fmt.Fprintf(w, "  %d. %-18s %-20s %s\n", i+1, in.Profile, in.Title, humanBytes(in.Bytes))
		}
		if !a.interactive() {
			fmt.Fprintln(w, "\nRemove one with: ragalay setup --remove <name> --yes")
			return nil
		}
		fmt.Fprint(w, "\nRemove which? Numbers separated by commas, or Enter for none: ")
		line, _ := bufio.NewReader(a.stdin).ReadString('\n')
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' }) {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(list) {
				remove = append(remove, list[n-1].Profile)
			}
		}
		if len(remove) == 0 {
			return nil
		}
		yes = true // chosen just now
	}
	for _, name := range remove {
		i := slices.IndexFunc(list, func(in setup.Installed) bool { return in.Profile == name })
		if i < 0 {
			return fmt.Errorf("%s is not installed", name)
		}
		if !yes {
			ok, err := a.confirm(fmt.Sprintf("Remove %s (%s)? Folders that use it will need \"ragalay setup\" again. Type yes: ", name, humanBytes(list[i].Bytes)), true)
			if err != nil || !ok {
				return err
			}
		}
		if err := setup.Remove(cache, name); err != nil {
			return err
		}
		fmt.Fprintf(w, "Removed %s (%s).\n", name, humanBytes(list[i].Bytes))
	}
	return nil
}

func isWindows() bool { return filepath.Separator == '\\' }

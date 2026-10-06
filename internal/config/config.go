// Package config loads, validates and saves .ragalay/config.toml and finds
// the ragalay root directory.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DirName    = ".ragalay"
	ConfigFile = "config.toml"
	DBFile     = "index.db"
	LogsDir    = "logs"
)

// Kinds that Plan 1 can ingest.
var supportedKinds = []string{"md", "pdf", "image"}

// Matryoshka dimensions supported by Jina v5.
var supportedDims = []int{1024, 768, 512, 256, 128, 64, 32}

type Config struct {
	Scan   Scan   `toml:"scan"`
	Pairs  []Pair `toml:"pairs"`
	Index  Index  `toml:"index"`
	Chunk  Chunk  `toml:"chunk"`
	Embed  Embed  `toml:"embed"`
	Cache  Cache  `toml:"cache"`
	Update Update `toml:"update"`
}

type Scan struct {
	// Folders to scan, slash-separated and relative to the root. Empty means
	// the whole root.
	Folders []string `toml:"folders"`
	// Keep lists folders that are no longer scanned but whose documents stay
	// searchable ("ragalay folders remove --keep"). New files there are not
	// picked up.
	Keep   []string `toml:"keep"`
	Ignore []string `toml:"ignore"`
	Kinds  []string `toml:"kinds"`
}

// Pair maps a folder of Markdown transcriptions to the folder of their PDFs.
type Pair struct {
	MD  string `toml:"md"`
	PDF string `toml:"pdf"`
}

type Index struct {
	// PDFPageImages embeds every PDF page as an image too. It finds scanned
	// pages and figures, but costs ~0.1 s per page on a GPU and ~6 s on a CPU.
	PDFPageImages bool `toml:"pdf_page_images"`
}

type Chunk struct {
	Tokens  int `toml:"tokens"`
	Overlap int `toml:"overlap"`
}

type Embed struct {
	Dim           int    `toml:"dim"`
	IndexModel    string `toml:"index_model"`
	IndexRevision string `toml:"index_revision"`
	QueryModel    string `toml:"query_model"`
	ImageMaxSide  int    `toml:"image_max_side"`
}

type Cache struct {
	QueryMax int      `toml:"query_max"`
	QueryTTL Duration `toml:"query_ttl"`
}

type Update struct {
	Check bool `toml:"check"`
}

// Duration is a time.Duration that reads and writes as a string ("720h").
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default returns the configuration written by `ragalay init`.
func Default() Config {
	return Config{
		Scan: Scan{
			Folders: []string{},
			Keep:    []string{},
			Ignore:  []string{"**/node_modules/**", "**/.git/**"},
			Kinds:   []string{"md", "pdf", "image"},
		},
		Index: Index{PDFPageImages: true},
		Chunk: Chunk{Tokens: 256, Overlap: 32},
		Embed: Embed{
			Dim:           1024,
			IndexModel:    "jinaai/jina-embeddings-v5-omni-small-retrieval",
			IndexRevision: "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4",
			QueryModel:    "jinaai/jina-embeddings-v5-text-small-retrieval-GGUF:Q8_0",
			ImageMaxSide:  1024,
		},
		Cache:  Cache{QueryMax: 10000, QueryTTL: Duration{720 * time.Hour}},
		Update: Update{Check: true},
	}
}

// Path returns the config file path for a root.
func Path(root string) string { return filepath.Join(root, DirName, ConfigFile) }

// Load reads the config for root. Keys missing from the file keep their
// defaults, so older config files keep working.
func Load(root string) (Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(Path(root), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", Path(root), err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("%s: unknown keys: %s", Path(root), strings.Join(keys, ", "))
	}
	if cfg.Scan.Folders == nil {
		cfg.Scan.Folders = []string{}
	}
	if cfg.Scan.Keep == nil {
		cfg.Scan.Keep = []string{}
	}
	return cfg, nil
}

const header = `# ragalay configuration. Edit freely; run "ragalay status" to check it.
# Docs: README.md
#
# [scan] folders: folders to index, relative to this directory. Empty = everything.
#                 Manage with "ragalay folders add|remove|list".
# [[pairs]]:      Markdown transcriptions and their PDFs in separate folders, e.g.
#                   [[pairs]]
#                   md = "md"
#                   pdf = "pdf"
# [embed] dim:    1024, 768, 512, 256, 128, 64 or 32. Changing it means a re-embed.

`

// Save writes cfg to root's config file. Comments other than the generated
// header are not preserved.
func Save(root string, cfg Config) error {
	var buf bytes.Buffer
	buf.WriteString(header)
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := Path(root) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(root))
}

// Validate reports every problem in cfg at once.
func (c Config) Validate() error {
	var errs []error
	for _, f := range c.Scan.Folders {
		if err := checkRelative(f); err != nil {
			errs = append(errs, fmt.Errorf("scan.folders: %q: %w", f, err))
		}
	}
	for _, f := range c.Scan.Keep {
		if err := checkRelative(f); err != nil {
			errs = append(errs, fmt.Errorf("scan.keep: %q: %w", f, err))
		}
	}
	for _, k := range c.Scan.Kinds {
		if !slices.Contains(supportedKinds, k) {
			errs = append(errs, fmt.Errorf("scan.kinds: %q is not one of %v", k, supportedKinds))
		}
	}
	for _, p := range c.Scan.Ignore {
		if _, err := filepath.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil {
			errs = append(errs, fmt.Errorf("scan.ignore: %q: %w", p, err))
		}
	}
	for i, p := range c.Pairs {
		if p.MD == "" || p.PDF == "" {
			errs = append(errs, fmt.Errorf("pairs[%d]: both md and pdf are required", i))
			continue
		}
		for _, f := range []string{p.MD, p.PDF} {
			if err := checkRelative(f); err != nil {
				errs = append(errs, fmt.Errorf("pairs[%d]: %q: %w", i, f, err))
			}
		}
	}
	if c.Chunk.Tokens < 32 || c.Chunk.Tokens > 8192 {
		errs = append(errs, fmt.Errorf("chunk.tokens: %d must be between 32 and 8192", c.Chunk.Tokens))
	}
	if c.Chunk.Overlap < 0 || c.Chunk.Overlap >= c.Chunk.Tokens {
		errs = append(errs, fmt.Errorf("chunk.overlap: %d must be >= 0 and smaller than chunk.tokens", c.Chunk.Overlap))
	}
	if !slices.Contains(supportedDims, c.Embed.Dim) {
		errs = append(errs, fmt.Errorf("embed.dim: %d is not one of %v", c.Embed.Dim, supportedDims))
	}
	if c.Embed.IndexModel == "" || c.Embed.QueryModel == "" {
		errs = append(errs, errors.New("embed: index_model and query_model are required"))
	}
	if c.Embed.ImageMaxSide < 224 || c.Embed.ImageMaxSide > 2048 {
		errs = append(errs, fmt.Errorf("embed.image_max_side: %d must be between 224 and 2048", c.Embed.ImageMaxSide))
	}
	if c.Cache.QueryMax < 0 {
		errs = append(errs, errors.New("cache.query_max must be >= 0"))
	}
	if c.Cache.QueryTTL.Duration < 0 {
		errs = append(errs, errors.New("cache.query_ttl must be >= 0"))
	}
	return errors.Join(errs...)
}

// checkRelative rejects stored paths that could point outside the root (G5).
func checkRelative(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if strings.Contains(p, `\`) {
		return errors.New("use forward slashes")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || filepath.VolumeName(p) != "" {
		return errors.New("must be relative to the ragalay directory")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return errors.New("must stay inside the ragalay directory")
		}
	}
	return nil
}

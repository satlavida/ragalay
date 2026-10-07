package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// Embed chooses the embedding model (plan2 §5.2).
type Embed struct {
	Profile      string  `toml:"profile"`
	Dim          int     `toml:"dim"`
	ImageMaxSide int     `toml:"image_max_side"`
	OpenAI       *OpenAI `toml:"openai,omitempty"`

	// Plan 1 keys. Load turns them into Profile; Save drops them.
	IndexModel    string `toml:"index_model,omitempty"`
	IndexRevision string `toml:"index_revision,omitempty"`
	QueryModel    string `toml:"query_model,omitempty"`
}

// Image input modes of an OpenAI-compatible service (plan2 P7).
const (
	ImageNone     = "none"
	ImageJina     = "jina"     // input: [{"image": "<data URI>"}]
	ImageVLLM     = "vllm"     // messages with image_url content parts
	ImageLlamaCpp = "llamacpp" // input: [{"prompt_string": marker, "multimodal_data": [b64]}]
)

var imageModes = []string{ImageNone, ImageJina, ImageVLLM, ImageLlamaCpp}

// OpenAI configures an OpenAI-compatible embeddings endpoint. The API key
// is read from the environment variable APIKeyEnv; config.toml never holds
// it (plan2 P6).
type OpenAI struct {
	BaseURL        string         `toml:"base_url"`
	Model          string         `toml:"model"`
	APIKeyEnv      string         `toml:"api_key_env"`
	Dimensions     int            `toml:"dimensions"` // >0: send "dimensions"
	Matryoshka     bool           `toml:"matryoshka"` // ragalay may truncate to dim itself
	ImageInput     string         `toml:"image_input"`
	QueryPrefix    string         `toml:"query_prefix"`
	DocumentPrefix string         `toml:"document_prefix"`
	QueryExtra     map[string]any `toml:"query_extra,omitempty"`    // merged into query requests
	DocumentExtra  map[string]any `toml:"document_extra,omitempty"` // merged into document requests
	BatchSize      int            `toml:"batch_size"`
	Concurrency    int            `toml:"concurrency"` // 0: 4 on this computer, 1 elsewhere (S16)
	MaxInputTokens int            `toml:"max_input_tokens"`
	Timeout        Duration       `toml:"timeout"`
	QueryTimeout   Duration       `toml:"query_timeout"`
}

// DefaultOpenAI is a local Ollama, the most common starting point.
func DefaultOpenAI() *OpenAI {
	return &OpenAI{
		BaseURL: "http://localhost:11434/v1", Model: "nomic-embed-text",
		ImageInput: ImageNone, BatchSize: 64, MaxInputTokens: 2000,
		Timeout: Duration{60 * time.Second}, QueryTimeout: Duration{5 * time.Second},
	}
}

// Host returns the endpoint's host[:port].
func (o *OpenAI) Host() string {
	u, err := url.Parse(o.BaseURL)
	if err != nil {
		return o.BaseURL
	}
	return u.Host
}

// Loopback reports whether the endpoint is on this computer. Only loopback
// counts as local: a LAN host may be anyone's server (plan2 S10).
func (o *OpenAI) Loopback() bool {
	u, err := url.Parse(o.BaseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// EffectiveConcurrency applies the default (S16).
func (o *OpenAI) EffectiveConcurrency() int {
	switch {
	case o.Concurrency > 0:
		return o.Concurrency
	case o.Loopback():
		return 4
	}
	return 1
}

// variant is everything besides model, host and dim that changes vectors.
func (o *OpenAI) variant() string {
	if o.QueryPrefix == "" && o.DocumentPrefix == "" && (o.ImageInput == "" || o.ImageInput == ImageNone) &&
		o.Dimensions == 0 && len(o.QueryExtra) == 0 && len(o.DocumentExtra) == 0 {
		return ""
	}
	qe, _ := json.Marshal(o.QueryExtra) // map keys are sorted
	de, _ := json.Marshal(o.DocumentExtra)
	return fmt.Sprintf("q=%q d=%q img=%s dims=%d qe=%s de=%s", o.QueryPrefix, o.DocumentPrefix, o.ImageInput,
		o.Dimensions, qe, de)
}

func (o *OpenAI) validate() []error {
	var errs []error
	u, err := url.Parse(o.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("embed.openai.base_url: %q must be an http(s) URL such as http://localhost:11434/v1", o.BaseURL))
	}
	if o.Model == "" {
		errs = append(errs, errors.New("embed.openai.model is required"))
	}
	if !slices.Contains(imageModes, o.ImageInput) {
		errs = append(errs, fmt.Errorf("embed.openai.image_input: %q is not one of %v", o.ImageInput, imageModes))
	}
	if o.Dimensions < 0 || o.BatchSize < 1 || o.Concurrency < 0 || o.MaxInputTokens < 32 {
		errs = append(errs, errors.New("embed.openai: dimensions >= 0, batch_size >= 1, concurrency >= 0 and max_input_tokens >= 32 are required"))
	}
	if o.Timeout.Duration <= 0 || o.QueryTimeout.Duration <= 0 {
		errs = append(errs, errors.New("embed.openai: timeout and query_timeout must be positive"))
	}
	if strings.ContainsAny(o.APIKeyEnv, " =$%") {
		errs = append(errs, fmt.Errorf("embed.openai.api_key_env: %q must be the NAME of an environment variable, not the key", o.APIKeyEnv))
	}
	return errs
}

// Lookup returns the configured profile.
func (e Embed) Lookup() (embed.Profile, error) {
	p, ok := embed.Lookup(e.Profile)
	if !ok {
		return p, fmt.Errorf("embed.profile: %q is not one of %v", e.Profile, embed.ProfileNames())
	}
	return p, nil
}

// SpaceID names the vector space these settings produce. Jina's ID is the
// one Plan 1 wrote, so existing folders keep their index.
func (e Embed) SpaceID() string {
	if e.Profile == embed.OpenAI {
		o := e.OpenAI
		if o == nil {
			o = DefaultOpenAI()
		}
		return embed.HTTPID(o.Model, o.Host(), e.Dim, o.variant())
	}
	p, _ := embed.Lookup(e.Profile)
	return embed.ID(p.IndexModel, p.IndexRevision, e.Dim)
}

// Remote reports whether embedding sends data to another computer.
func (e Embed) Remote() bool {
	return e.Profile == embed.OpenAI && e.OpenAI != nil && !e.OpenAI.Loopback()
}

// Modalities the settings can embed as vectors.
func (e Embed) Modalities() []string {
	if e.Profile == embed.OpenAI {
		if e.OpenAI == nil || e.OpenAI.ImageInput == ImageNone || e.OpenAI.ImageInput == "" {
			return []string{embed.Text}
		}
		return []string{embed.Text, embed.Image}
	}
	p, _ := embed.Lookup(e.Profile)
	return p.Modalities
}

// UseProfile switches to profile name with dim (0 = the profile's default).
func (e Embed) UseProfile(name string, dim int) (Embed, error) {
	p, ok := embed.Lookup(name)
	if !ok {
		return e, fmt.Errorf("unknown model %q (choose one of %v)", name, embed.ProfileNames())
	}
	e.Profile = name
	if dim == 0 {
		dim = p.DefaultDim
	}
	if name == embed.OpenAI && e.OpenAI == nil {
		e.OpenAI = DefaultOpenAI()
	}
	e.Dim = dim
	return e, nil
}

// migrate turns Plan 1 keys into a profile. Plan 1 always wrote
// index_model, so every Plan 1 folder keeps Jina (plan2 §5.2).
func (e *Embed) migrate(profileSet bool) {
	if !profileSet && e.IndexModel != "" {
		if strings.HasPrefix(e.IndexModel, "jinaai/") {
			e.Profile = embed.JinaV5
		}
	}
	e.IndexModel, e.IndexRevision, e.QueryModel = "", "", ""
}

func (e Embed) validate() []error {
	var errs []error
	p, err := e.Lookup()
	if err != nil {
		return append(errs, err)
	}
	if !p.AllowsDim(e.Dim) {
		if p.Dims == nil {
			errs = append(errs, fmt.Errorf("embed.dim: %d must be between 1 and 8192", e.Dim))
		} else {
			errs = append(errs, fmt.Errorf("embed.dim: %d is not one of %v for %s", e.Dim, p.Dims, p.Name))
		}
	}
	if e.ImageMaxSide < 224 || e.ImageMaxSide > 2048 {
		errs = append(errs, fmt.Errorf("embed.image_max_side: %d must be between 224 and 2048", e.ImageMaxSide))
	}
	if p.Name == embed.OpenAI {
		if e.OpenAI == nil {
			errs = append(errs, errors.New("embed.openai: section required for profile openai"))
		} else {
			errs = append(errs, e.OpenAI.validate()...)
		}
	}
	return errs
}

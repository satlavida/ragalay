// Package embed defines what ragalay embeds, the embedding profiles, and the
// vector helpers shared by the indexing runtime (Python sidecar or an
// OpenAI-compatible service) and the query runtime (llama.cpp or the same
// service). Both produce vectors in the same space.
package embed

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Modalities of an Input.
const (
	Text    = "text"
	Image   = "image"
	PDFPage = "pdf_page"
)

// Input is one thing to embed as a document.
type Input struct {
	Modality string `json:"modality"`
	Text     string `json:"text,omitempty"`  // text, or the caption for a combined text+image input
	Path     string `json:"path,omitempty"`  // image or PDF file
	Page     int    `json:"page,omitempty"`  // 0-based page for pdf_page
	Title    string `json:"title,omitempty"` // file name + heading path, for title prompts
}

// Indexer embeds documents (any modality). Used only while indexing.
type Indexer interface {
	EmbedDocuments(ctx context.Context, in []Input) ([][]float32, error)
	Close() error
}

// Querier embeds search queries (text only). Must not need Python.
type Querier interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Close() error
}

// ID names the vector space of a local profile's index. Documents embedded
// under a different ID must be re-embedded (plan1 §4.6). The query model is
// not part of it: it shares the space by construction.
func ID(model, revision string, dim int) string {
	if len(revision) > 7 {
		revision = revision[:7]
	}
	return fmt.Sprintf("%s@%s:%d", model, revision, dim)
}

// Fit truncates v to dim (Matryoshka) and L2-normalizes it in place.
func Fit(v []float32, dim int) ([]float32, error) {
	if len(v) < dim {
		return nil, fmt.Errorf("vector has %d dimensions, need at least %d", len(v), dim)
	}
	v = v[:dim]
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return nil, errors.New("zero vector")
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
	return v, nil
}

// Cosine returns the cosine similarity of a and b.
func Cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return d / math.Sqrt(na*nb)
}

// Blob encodes v as little-endian float32, the F32_BLOB layout Turso uses.
func Blob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// FromBlob decodes a Blob.
func FromBlob(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob has %d bytes, not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

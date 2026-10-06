// Package chunk splits extracted text into retrieval-sized chunks
// (plan1 G16: ~256 tokens, 32 overlap, heading-first).
package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tokenizer counts tokens. Indexing uses the real model tokenizer (the
// query model shares the omni text tower's vocabulary); tests use Estimate.
type Tokenizer interface {
	Count(text string) int
}

// Estimate approximates tokens for English-like text (~4 characters or
// 0.75 words per token), deterministic and dependency-free.
type Estimate struct{}

func (Estimate) Count(s string) int {
	words := len(strings.Fields(s))
	byChars := (utf8.RuneCountInString(s) + 3) / 4
	byWords := (words*4 + 2) / 3
	return max(byChars, byWords)
}

// Section is a run of text with one heading path (a Markdown section or a
// PDF page).
type Section struct {
	HeadingPath string
	Text        string
	Page        int // 1-based, 0 = unknown
}

// Chunk is one piece to embed.
type Chunk struct {
	HeadingPath string `json:"heading_path,omitempty"`
	Text        string `json:"text"`
	Page        int    `json:"page,omitempty"`
	Tokens      int    `json:"tokens"`
}

// Split chunks sections. Sections are never merged across pages; very small
// sections (under minTokens) are merged into the next one so headings with
// a line of text do not become tiny chunks.
func Split(sections []Section, maxTokens, overlap int, tok Tokenizer) []Chunk {
	minTokens := maxTokens / 4
	var out []Chunk
	var pending *Section
	flush := func(s Section) {
		out = append(out, splitSection(s, maxTokens, overlap, tok)...)
	}
	for _, s := range sections {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text == "" {
			continue
		}
		if pending != nil {
			if pending.Page == s.Page && tok.Count(pending.Text)+tok.Count(s.Text) <= maxTokens {
				pending.Text += "\n\n" + headingLine(s.HeadingPath, pending.HeadingPath) + s.Text
				if tok.Count(pending.Text) >= minTokens {
					flush(*pending)
					pending = nil
				}
				continue
			}
			flush(*pending)
			pending = nil
		}
		if tok.Count(s.Text) < minTokens {
			cp := s
			pending = &cp
			continue
		}
		flush(s)
	}
	if pending != nil {
		flush(*pending)
	}
	return out
}

// headingLine returns the last heading of path as a Markdown line when it
// differs from the section it is merged into, so merged text keeps its
// structure.
func headingLine(path, into string) string {
	if path == "" || path == into {
		return ""
	}
	parts := strings.Split(path, " > ")
	return "## " + parts[len(parts)-1] + "\n"
}

func splitSection(s Section, maxTokens, overlap int, tok Tokenizer) []Chunk {
	if n := tok.Count(s.Text); n <= maxTokens {
		return []Chunk{{HeadingPath: s.HeadingPath, Text: s.Text, Page: s.Page, Tokens: n}}
	}
	pieces := atomize(s.Text, maxTokens, tok)
	var out []Chunk
	var cur []string
	curTokens := 0
	emit := func() {
		text := strings.TrimSpace(strings.Join(cur, ""))
		if text != "" {
			out = append(out, Chunk{HeadingPath: s.HeadingPath, Text: text, Page: s.Page, Tokens: tok.Count(text)})
		}
	}
	for _, p := range pieces {
		pt := tok.Count(p)
		if curTokens > 0 && curTokens+pt > maxTokens {
			emit()
			tail := overlapTail(strings.Join(cur, ""), overlap, tok)
			cur = []string{tail}
			curTokens = tok.Count(tail)
			if curTokens+pt > maxTokens { // overlap would not fit with this piece
				cur, curTokens = nil, 0
			}
		}
		cur = append(cur, p)
		curTokens += pt
	}
	emit()
	return out
}

// atomize breaks text into pieces of at most maxTokens, preferring paragraph,
// then sentence, then word boundaries. Pieces keep their trailing separators
// so joining them restores the text.
func atomize(text string, maxTokens int, tok Tokenizer) []string {
	var out []string
	for _, para := range splitKeep(text, "\n\n") {
		if tok.Count(para) <= maxTokens {
			out = append(out, para)
			continue
		}
		for _, sent := range sentences(para) {
			if tok.Count(sent) <= maxTokens {
				out = append(out, sent)
				continue
			}
			out = append(out, wordPieces(sent, maxTokens, tok)...)
		}
	}
	return out
}

func splitKeep(s, sep string) []string {
	var out []string
	for {
		i := strings.Index(s, sep)
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+len(sep)])
		s = s[i+len(sep):]
	}
}

// sentences splits after ., ! or ? followed by whitespace, and at newlines.
func sentences(p string) []string {
	var out []string
	start := 0
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		end := false
		switch {
		case rs[i] == '\n':
			end = true
		case (rs[i] == '.' || rs[i] == '!' || rs[i] == '?') && i+1 < len(rs) && unicode.IsSpace(rs[i+1]):
			end = true
			i++ // keep the space with this sentence
		}
		if end {
			out = append(out, string(rs[start:i+1]))
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

func wordPieces(s string, maxTokens int, tok Tokenizer) []string {
	var out []string
	var cur strings.Builder
	for _, w := range strings.SplitAfter(s, " ") {
		if cur.Len() > 0 && tok.Count(cur.String()+w) > maxTokens {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// overlapTail returns the end of text, whole words only, of at most
// overlap tokens.
func overlapTail(text string, overlap int, tok Tokenizer) string {
	if overlap <= 0 {
		return ""
	}
	words := strings.Fields(text)
	i := len(words)
	for i > 0 && tok.Count(strings.Join(words[i-1:], " ")) <= overlap {
		i--
	}
	if i == len(words) {
		return ""
	}
	return strings.Join(words[i:], " ") + " "
}

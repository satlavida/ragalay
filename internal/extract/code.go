package extract

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/embed"
)

// Code splits a source file into passages of whole lines. Breaks prefer a
// blank line, and the next passage repeats the last few lines for context.
// Each passage is labelled "file.go:40-88" so results point at the lines
// (plan3, code files). No parser is involved, so every language works.
func Code(rel string, src []byte, maxTokens, overlap int, tok chunk.Tokenizer) ([]Unit, error) {
	if bytes.IndexByte(src, 0) >= 0 || !utf8.Valid(src) {
		return nil, errors.New("not a text file")
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > 0 && len(src)/len(lines) > 300 {
		return nil, errors.New("looks minified or generated (very long lines)")
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("the file is empty")
	}
	if maxTokens <= 0 {
		maxTokens = 256
	}
	label := path.Base(rel)

	var units []Unit
	emit := func(from, to int) { // 0-based, [from, to)
		body := strings.Join(lines[from:to], "\n")
		if strings.TrimSpace(body) == "" {
			return
		}
		units = append(units, Unit{Modality: embed.Text, Text: body, Tokens: tok.Count(body),
			HeadingPath: fmt.Sprintf("%s:%d-%d", label, from+1, to)})
	}
	start := 0
	for start < len(lines) {
		end, tokens, lastBlank := start, 0, -1
		for end < len(lines) {
			t := tok.Count(lines[end]) + 1
			if tokens+t > maxTokens && end > start {
				break
			}
			tokens += t
			if strings.TrimSpace(lines[end]) == "" && end > start {
				lastBlank = end
			}
			end++
		}
		if end < len(lines) && lastBlank > start+(end-start)/3 {
			end = lastBlank + 1 // break after a blank line, not mid-block
		}
		emit(start, end)
		if end >= len(lines) {
			break
		}
		next := end
		for next > start+1 && overlapTokens(lines[next-1:end], tok) <= overlap {
			next--
		}
		if next >= end || next <= start {
			next = end
		}
		start = next
	}
	if len(units) == 0 {
		return nil, errors.New("the file is empty")
	}
	return units, nil
}

func overlapTokens(lines []string, tok chunk.Tokenizer) int {
	return tok.Count(strings.Join(lines, "\n"))
}

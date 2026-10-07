package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/satlavida/ragalay/internal/embed"
)

// loadImage reads an image file, scales it to fit maxSide and returns it as
// PNG bytes.
func loadImage(path string, maxSide int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("cannot read the image: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxSide || h > maxSide {
		if w >= h {
			w, h = maxSide, max(1, h*maxSide/w)
		} else {
			w, h = max(1, w*maxSide/h), maxSide
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, src)
	return buf.Bytes(), err
}

// embedImage embeds one image file with the configured extension.
func (c *Client) embedImage(ctx context.Context, in embed.Input) ([]float32, error) {
	pngData, err := loadImage(in.Path, c.o.MaxSide)
	if err != nil {
		return nil, err
	}
	b64 := base64.StdEncoding.EncodeToString(pngData)
	uri := "data:image/png;base64," + b64
	body := c.body(c.o.DocumentExtra)
	switch c.o.ImageInput {
	case ImageJina:
		// Jina's API embeds an image item on its own; the caption is in the
		// keyword index already.
		body["input"] = []map[string]string{{"image": uri}}
	case ImageVLLM:
		content := []map[string]any{{"type": "image_url", "image_url": map[string]string{"url": uri}}}
		if in.Text != "" {
			content = append(content, map[string]any{"type": "text", "text": c.o.DocumentPrefix + in.Text})
		}
		body["messages"] = []map[string]any{{"role": "user", "content": content}}
	case ImageLlamaCpp:
		marker, err := c.mediaMarker(ctx)
		if err != nil {
			return nil, err
		}
		prompt := c.o.DocumentPrefix
		if in.Text != "" {
			prompt += in.Text + " "
		}
		body["input"] = []map[string]any{{"prompt_string": prompt + marker, "multimodal_data": []string{b64}}}
	default:
		return nil, fmt.Errorf("unknown image input %q", c.o.ImageInput)
	}
	vs, err := c.request(ctx, body, 1)
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// mediaMarker asks llama-server for its media marker, which is random per
// server start (GET /props, plan2 §3.1).
func (c *Client) mediaMarker(ctx context.Context) (string, error) {
	c.mu.Lock()
	m := c.marker
	c.mu.Unlock()
	if m != "" {
		return m, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverRoot(c.o.BaseURL)+"/props", nil)
	if err != nil {
		return "", err
	}
	if c.o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.o.APIKey)
	}
	resp, err := c.o.HTTP.Do(req)
	if err != nil {
		return "", redact(err, c.o.APIKey)
	}
	defer resp.Body.Close()
	var props struct {
		Marker     string `json:"media_marker"`
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&props); err != nil || resp.StatusCode != http.StatusOK {
		return "", errors.New("could not read the server's /props (image_input = \"llamacpp\" needs llama-server)")
	}
	if !props.Modalities.Vision {
		return "", errors.New("llama-server was started without a vision projector (--mmproj)")
	}
	if props.Marker == "" {
		props.Marker = "<__media__>" // older llama-server builds
	}
	c.mu.Lock()
	c.marker = props.Marker
	c.mu.Unlock()
	return props.Marker, nil
}

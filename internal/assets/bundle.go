// Package assets concatenates and pre-compresses the front-end JS bundle at
// startup so each HTTP response just copies a byte slice.
package assets

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

type Bundle struct {
	raw     []byte
	gz      []byte
	br      []byte
	version string
	etag    string
	modTime time.Time
}

// NewBundle reads files in order, joins them with ';\n' (safe between IIFEs),
// and precomputes gzip + brotli bodies. Startup cost is ~100ms on rk3566.
func NewBundle(root string, files []string) (*Bundle, error) {
	var buf bytes.Buffer
	for i, name := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("bundle read %s: %w", name, err)
		}
		if i > 0 {
			buf.WriteString(";\n")
		}
		buf.Write(data)
	}
	raw := buf.Bytes()

	var gzBuf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&gzBuf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(raw); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	var brBuf bytes.Buffer
	bw := brotli.NewWriterLevel(&brBuf, brotli.BestCompression)
	if _, err := bw.Write(raw); err != nil {
		return nil, err
	}
	if err := bw.Close(); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	ver := hex.EncodeToString(sum[:6])
	return &Bundle{
		raw:     raw,
		gz:      gzBuf.Bytes(),
		br:      brBuf.Bytes(),
		version: ver,
		etag:    `"` + ver + `"`,
		modTime: time.Now(),
	}, nil
}

// Version returns a short content hash used as the cache-busting query param.
func (b *Bundle) Version() string { return b.version }

// Stats returns raw/gzip/brotli byte sizes for logging.
func (b *Bundle) Stats() (raw, gz, br int) { return len(b.raw), len(b.gz), len(b.br) }

// Handler serves the bundle with Content-Encoding chosen from Accept-Encoding.
// ETag + immutable caching means browsers hit 304 on subsequent navigations.
func (b *Bundle) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("Vary", "Accept-Encoding")
		h.Set("ETag", b.etag)
		if r.Header.Get("If-None-Match") == b.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		ae := r.Header.Get("Accept-Encoding")
		switch {
		case strings.Contains(ae, "br"):
			h.Set("Content-Encoding", "br")
			_, _ = w.Write(b.br)
		case strings.Contains(ae, "gzip"):
			h.Set("Content-Encoding", "gzip")
			_, _ = w.Write(b.gz)
		default:
			_, _ = w.Write(b.raw)
		}
	}
}

// Package imageutil decodes user-uploaded images (JPEG/PNG/WebP/HEIC),
// strips EXIF via re-encode, and produces two variants (full + thumbnail).
//
// Hybrid encoder: every variant is encoded BOTH as JPEG (q=85) and as lossless
// WebP (VP8L) in parallel; the smaller of the two is kept. Rationale:
// photos compress best with lossy DCT (JPEG), flat-color/screenshot content
// compresses best with lossless palette coding (WebP VP8L). Picking per-image
// avoids screenshot-becomes-bigger-than-source cases that plagued pure-JPEG.
package imageutil

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"sync"

	"github.com/HugoSmits86/nativewebp"
	"github.com/jdeng/goheif"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	FullMaxEdge  = 2048
	ThumbMaxEdge = 480
	JPEGQuality  = 85
)

var ErrUnsupported = errors.New("unsupported image format")

// Accepted input MIME types (for client-side hints + server validation).
var AcceptedMimes = map[string]bool{
	"image/jpeg": true,
	"image/jpg":  true,
	"image/png":  true,
	"image/webp": true,
	"image/heic": true,
	"image/heif": true,
}

// DetectMime sniffs the first 512 bytes plus a HEIC/HEIF ftyp check.
// http.DetectContentType alone does not recognise HEIC reliably.
func DetectMime(head []byte) string {
	// HEIC/HEIF ftyp box: bytes 4..8 == "ftyp", bytes 8..12 ∈ {heic,heix,hevc,hevx,mif1,msf1}.
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		brand := string(head[8:12])
		switch brand {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs":
			return "image/heic"
		case "mif1", "msf1":
			return "image/heif"
		}
	}
	return http.DetectContentType(head)
}

// Decode dispatches to the right decoder based on sniffed MIME.
// HEIC must go through goheif since stdlib/x/image don't handle it.
func Decode(data []byte) (image.Image, error) {
	mime := DetectMime(data[:min(len(data), 64)])
	if !AcceptedMimes[mime] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, mime)
	}
	if mime == "image/heic" || mime == "image/heif" {
		img, err := goheif.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("heic decode: %w", err)
		}
		return img, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("image decode: %w", err)
	}
	return img, nil
}

// Resize scales the image so neither side exceeds maxEdge.
// Returns the original if it already fits.
func Resize(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxEdge
		nh = h * maxEdge / w
	} else {
		nh = maxEdge
		nw = w * maxEdge / h
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// CatmullRom: best quality bicubic in x/image/draw for downscaling.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// EncodeJPEG writes the image as JPEG to w.
func EncodeJPEG(w io.Writer, img image.Image) error {
	return jpeg.Encode(w, img, &jpeg.Options{Quality: JPEGQuality})
}

// EncodeWebPLossless writes the image as lossless WebP (VP8L).
func EncodeWebPLossless(w io.Writer, img image.Image) error {
	return nativewebp.Encode(w, img, nil)
}

// Variant is the output bytes + dimensions + format of one encoded variant.
type Variant struct {
	Bytes  []byte
	Width  int
	Height int
	Ext    string // "jpg" | "webp"
	Mime   string // "image/jpeg" | "image/webp"
}

// ProcessResult holds the two output variants plus source dimensions.
type ProcessResult struct {
	Full      Variant
	Thumb     Variant
	SrcWidth  int
	SrcHeight int
	SrcMime   string
}

// encodeSmallest runs JPEG and lossless-WebP encoders in parallel and returns
// the smaller of the two. If both fail, returns the first error encountered.
func encodeSmallest(img image.Image) (Variant, error) {
	var (
		jpgBuf, webpBuf bytes.Buffer
		jpgErr, webpErr error
		wg              sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); jpgErr = EncodeJPEG(&jpgBuf, img) }()
	go func() { defer wg.Done(); webpErr = EncodeWebPLossless(&webpBuf, img) }()
	wg.Wait()

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// Pick the smaller successfully-encoded buffer.
	jpgOK, webpOK := jpgErr == nil, webpErr == nil
	if jpgOK && webpOK {
		if webpBuf.Len() < jpgBuf.Len() {
			return Variant{Bytes: webpBuf.Bytes(), Width: w, Height: h, Ext: "webp", Mime: "image/webp"}, nil
		}
		return Variant{Bytes: jpgBuf.Bytes(), Width: w, Height: h, Ext: "jpg", Mime: "image/jpeg"}, nil
	}
	if jpgOK {
		return Variant{Bytes: jpgBuf.Bytes(), Width: w, Height: h, Ext: "jpg", Mime: "image/jpeg"}, nil
	}
	if webpOK {
		return Variant{Bytes: webpBuf.Bytes(), Width: w, Height: h, Ext: "webp", Mime: "image/webp"}, nil
	}
	return Variant{}, fmt.Errorf("jpeg: %v; webp: %v", jpgErr, webpErr)
}

// Process decodes input, resizes to both variants, encodes each with the
// hybrid encoder, and keeps whichever format (JPEG or lossless WebP) produces
// fewer bytes per variant.
func Process(data []byte) (*ProcessResult, error) {
	mime := DetectMime(data[:min(len(data), 64)])
	src, err := Decode(data)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()

	full := Resize(src, FullMaxEdge)
	thumb := Resize(src, ThumbMaxEdge)

	fullVar, err := encodeSmallest(full)
	if err != nil {
		return nil, fmt.Errorf("encode full: %w", err)
	}
	thumbVar, err := encodeSmallest(thumb)
	if err != nil {
		return nil, fmt.Errorf("encode thumb: %w", err)
	}
	return &ProcessResult{
		Full:      fullVar,
		Thumb:     thumbVar,
		SrcWidth:  sb.Dx(),
		SrcHeight: sb.Dy(),
		SrcMime:   mime,
	}, nil
}

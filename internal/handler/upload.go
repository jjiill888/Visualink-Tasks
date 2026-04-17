package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"featuretrack/internal/db"
	"featuretrack/internal/imageutil"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

// UploadRoot is the filesystem directory where processed images live.
// Overridable via env UPLOAD_DIR for tests/deploys.
var UploadRoot = func() string {
	if v := os.Getenv("UPLOAD_DIR"); v != "" {
		return v
	}
	return "./data/uploads"
}()

const (
	maxUploadBytes = 25 << 20 // 25 MiB raw input — HEIC can be chunky
	sniffBytes     = 512
)

func randomSlug() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// UploadImage handles POST /uploads/image — multipart/form-data with field "file".
// Returns JSON { id, thumb_url, full_url, width, height }.
func UploadImage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			http.Error(w, "文件过大或上传失败（限 25 MiB）", http.StatusRequestEntityTooLarge)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "未收到文件", http.StatusBadRequest)
			return
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "读取文件失败", http.StatusInternalServerError)
			return
		}
		sniff := data
		if len(sniff) > sniffBytes {
			sniff = sniff[:sniffBytes]
		}
		if mime := imageutil.DetectMime(sniff); !imageutil.AcceptedMimes[mime] {
			http.Error(w, "不支持的图片格式（仅接受 JPEG/PNG/WebP/HEIC）", http.StatusUnsupportedMediaType)
			return
		}

		result, err := imageutil.Process(data)
		if err != nil {
			http.Error(w, "图片解码失败："+err.Error(), http.StatusBadRequest)
			return
		}

		now := time.Now()
		subdir := filepath.Join(fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%02d", now.Month()))
		slug := randomSlug()
		relFull := filepath.ToSlash(filepath.Join(subdir, slug+"-full."+result.Full.Ext))
		relThumb := filepath.ToSlash(filepath.Join(subdir, slug+"-thumb."+result.Thumb.Ext))
		absDir := filepath.Join(UploadRoot, subdir)
		if err := os.MkdirAll(absDir, 0o755); err != nil {
			http.Error(w, "存储目录创建失败", http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(UploadRoot, relFull), result.Full.Bytes, 0o644); err != nil {
			http.Error(w, "写入失败", http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(UploadRoot, relThumb), result.Thumb.Bytes, 0o644); err != nil {
			// Clean up full to avoid dangling file
			_ = os.Remove(filepath.Join(UploadRoot, relFull))
			http.Error(w, "写入失败", http.StatusInternalServerError)
			return
		}

		a := &model.Attachment{
			UploaderID: u.ID,
			PathFull:   relFull,
			PathThumb:  relThumb,
			Width:      result.Full.Width,
			Height:     result.Full.Height,
			Bytes:      int64(len(result.Full.Bytes) + len(result.Thumb.Bytes)),
			Original:   strings.TrimSpace(header.Filename),
		}
		if err := database.CreateAttachment(a); err != nil {
			_ = os.Remove(filepath.Join(UploadRoot, relFull))
			_ = os.Remove(filepath.Join(UploadRoot, relThumb))
			http.Error(w, "数据库写入失败", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        a.ID,
			"thumb_url": a.ThumbURL(),
			"full_url":  a.FullURL(),
			"width":     a.Width,
			"height":    a.Height,
		})
	}
}

// ServeUpload handles GET /uploads/{id}/{variant}. Variant: "thumb" | "full".
// Images are immutable per id so we send aggressive caching headers.
func ServeUpload(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		variant := chi.URLParam(r, "variant")
		a, err := database.GetAttachment(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var rel string
		switch variant {
		case "thumb":
			rel = a.PathThumb
		case "full":
			rel = a.PathFull
		default:
			http.Error(w, "bad variant", http.StatusBadRequest)
			return
		}
		// Prevent path traversal: we only store slash-separated rel paths generated
		// server-side, but guard anyway against future schema changes.
		if strings.Contains(rel, "..") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		abs := filepath.Join(UploadRoot, filepath.FromSlash(rel))
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		// Infer content type from extension — hybrid encoder may emit jpg or webp.
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".webp":
			w.Header().Set("Content-Type", "image/webp")
		default:
			w.Header().Set("Content-Type", "image/jpeg")
		}
		http.ServeFile(w, r, abs)
	}
}

// visualink 主程序:纯装配——打开数据库、构建静态资产、初始化各模块模板、
// 挂载各模块路由。业务代码全部在 internal/{tasks,notes,im,notification} 与
// internal/platform/* 中,main 不承载任何业务逻辑。
// 注意:模板与静态资源按相对路径读取,二进制必须在仓库根目录运行。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"visualink/internal/db"
	"visualink/internal/im"
	"visualink/internal/notes"
	"visualink/internal/notification"
	"visualink/internal/platform/assets"
	"visualink/internal/platform/auth"
	"visualink/internal/platform/hub"
	"visualink/internal/platform/upload"
	"visualink/internal/platform/web"
	"visualink/internal/tasks"

	httpcompression "github.com/CAFxX/httpcompression"
	brotlihttp "github.com/CAFxX/httpcompression/contrib/andybalholm/brotli"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/app.db"
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatal("create data dir:", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatal("open db:", err)
	}

	// ── 静态资产:先于模板解析构建,版本号注入 web 包供模板函数读取 ──────
	jsBundle, err := assets.NewBundle("static/js", []string{
		"htmx.min.js",
		"htmx-ext-sse.min.js",
		"idiomorph-ext.min.js",
		"htmx-ext-bundle.min.js",
		"alpine.min.js",
		"attachment-uploader.js",
	})
	if err != nil {
		log.Fatal("bundle js:", err)
	}
	rawSize, gzSize, brSize := jsBundle.Stats()
	log.Printf("js bundle: raw=%dB gzip=%dB brotli=%dB version=%s",
		rawSize, gzSize, brSize, jsBundle.Version())

	// 笔记编辑器岛(esbuild 产物,构建方式见 web/notes-editor/)。
	notesBundle, err := assets.NewBundle("static", []string{"notes-editor.js"})
	if err != nil {
		log.Fatal("bundle notes editor (先运行 web/notes-editor 下的 npm run build):", err)
	}
	cssBytes, err := os.ReadFile("static/css/notes-editor.css")
	if err != nil {
		log.Fatal("read static/css/notes-editor.css:", err)
	}
	cssSum := sha256.Sum256(cssBytes)
	// 全系统设计词元:启动时读入、模板函数内联进各页 <head>,零网络请求
	baseCSSBytes, err := os.ReadFile("static/css/ne-base.css")
	if err != nil {
		log.Fatal("read static/css/ne-base.css:", err)
	}
	nRaw, nGz, nBr := notesBundle.Stats()
	log.Printf("notes editor bundle: raw=%dB gzip=%dB brotli=%dB version=%s",
		nRaw, nGz, nBr, notesBundle.Version())
	// ESM 分割 chunk 清单(模板 modulepreload 防高 RTT 串行发现)
	var notesEditorChunks []string
	if chunks, err := filepath.Glob("static/ne-collab-*.js"); err == nil {
		for _, c := range chunks {
			notesEditorChunks = append(notesEditorChunks, filepath.Base(c))
		}
	}
	log.Printf("notes editor chunks: %v", notesEditorChunks)

	web.SetAssets(web.Assets{
		BundleVer:         jsBundle.Version(),
		NotesEditorVer:    notesBundle.Version(),
		NotesEditorCSSVer: hex.EncodeToString(cssSum[:6]),
		NeBaseCSS:         template.CSS(baseCSSBytes),
		NotesEditorChunks: notesEditorChunks,
	})

	// ── 各模块模板:谁的页面谁解析(隔离模板集,无跨模块混装) ──────────
	auth.InitTemplates()
	tasks.InitTemplates()
	notes.InitTemplates()
	im.InitTemplates()
	notification.InitTemplates()

	// ── 压缩:brotli 对中文用户比 gzip 再省 ~20%(180ms RTT 下可感) ────
	brComp, err := brotlihttp.New(brotlihttp.Options{Quality: 5})
	if err != nil {
		log.Fatal("brotli init:", err)
	}
	compress, err := httpcompression.DefaultAdapter(
		httpcompression.Compressor(brotlihttp.Encoding, 1000, brComp),
		// SSE 必须豁免:压缩器会把小事件闷在编码缓冲区里(浏览器 EventSource
		// 恒带 Accept-Encoding),导致实时推送整体失效——事件字节数被 Logger
		// 记为已写出,但客户端收不到。curl 不带压缩头时走透传,极易误判为正常
		httpcompression.ContentTypes([]string{"text/event-stream"}, true),
	)
	if err != nil {
		log.Fatal("compression adapter:", err)
	}

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(compress)

	// 预压缩 bundle 精确路由优先于 /static/* 通配
	r.Get("/static/js/bundle.js", jsBundle.Handler())
	r.Get("/static/notes-editor.js", notesBundle.Handler())

	// 静态文件长缓存(部署之间内容 hash 变了才换 URL)
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		staticFS.ServeHTTP(w, r)
	}))

	// 公开路由:登录/注册/退出
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	auth.Routes(r, database)

	// 登录后路由:各模块自挂
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return auth.RequireAuth(database, next)
		})

		r.Get("/sse", hub.SSE())
		tasks.Routes(r, database)
		notification.Routes(r, database)
		im.Routes(r, database)
		notes.Routes(r, database)
		upload.Routes(r, database)
	})

	// 后台任务
	tasks.StartAutoArchive(database)

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" { // 本地开发可覆盖端口,默认行为不变
		addr = ":" + p
	}
	log.Println("Listening on", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second, // 高延迟 VPN 下保持 keep-alive
	}
	log.Fatal(srv.ListenAndServe())
}

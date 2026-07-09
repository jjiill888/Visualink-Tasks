package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"featuretrack/internal/assets"
	"featuretrack/internal/db"
	"featuretrack/internal/handler"
	"featuretrack/internal/hub"
	"featuretrack/internal/model"

	httpcompression "github.com/CAFxX/httpcompression"
	brotlihttp "github.com/CAFxX/httpcompression/contrib/andybalholm/brotli"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// bundleVer is set in main() before templates are parsed; funcMap's
// bundleVersion reads it via closure.
var bundleVer string

// notesEditorVer / notesEditorCSSVer：笔记编辑器岛 JS/CSS 的内容 hash，
// 同样在 main() 中先于模板解析赋值，用作缓存穿透版本号。
var notesEditorVer, notesEditorCSSVer string

var mentionHighlightRe = regexp.MustCompile(`@([\p{L}\p{N}_]+)`)

var funcMap = template.FuncMap{
	"add":                   func(a, b int) int { return a + b },
	"bundleVersion":         func() string { return bundleVer },
	"notesEditorVersion":    func() string { return notesEditorVer },
	"notesEditorCSSVersion": func() string { return notesEditorCSSVer },
	"firstChar": func(s string) string {
		for _, r := range s {
			return string(r)
		}
		return "?"
	},
	"deref": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"map": func(kvs ...any) map[string]any {
		m := make(map[string]any, len(kvs)/2)
		for i := 0; i+1 < len(kvs); i += 2 {
			if k, ok := kvs[i].(string); ok {
				m[k] = kvs[i+1]
			}
		}
		return m
	},
	"highlightMentions": func(s string) template.HTML {
		var buf strings.Builder
		last := 0
		for _, loc := range mentionHighlightRe.FindAllStringIndex(s, -1) {
			buf.WriteString(template.HTMLEscapeString(s[last:loc[0]]))
			buf.WriteString(`<span class="mention">`)
			buf.WriteString(template.HTMLEscapeString(s[loc[0]:loc[1]]))
			buf.WriteString(`</span>`)
			last = loc[1]
		}
		buf.WriteString(template.HTMLEscapeString(s[last:]))
		return template.HTML(buf.String())
	},
}

// buildTmplMap parses one isolated template set per full-page template.
// This prevents {{define "content"}} from colliding across pages.
func buildTmplMap() map[string]*template.Template {
	// Pages that extend base.html and need feature_row + group partials
	withRow := []string{"dashboard.html", "mine.html", "group_detail.html"}
	// Pages that extend base.html, no partials needed
	plain := []string{"login.html", "register.html", "groups.html", "submit_standalone.html",
		"note_revision.html"}

	m := make(map[string]*template.Template)
	// note_edit.html 是独立标签页（Typora 式极简编辑器），不套 base.html 外壳；
	// 侧栏「文件」视图片段随页直出（同一片段也被 /notes/panel 用于刷新）
	m["note_edit.html"] = template.Must(template.New("").Funcs(funcMap).ParseFiles(
		"templates/note_edit.html",
		"templates/notes_panel_partial.html",
	))
	// notes.html 也是独立页（极简文库风格，与编辑页同一套 ne-* 设计语言），
	// 不套 base.html；内嵌列表片段（搜索时 HTMX 单独刷新该片段）
	m["notes.html"] = template.Must(template.New("").Funcs(funcMap).ParseFiles(
		"templates/notes.html",
		"templates/notes_list_partial.html",
	))
	for _, page := range withRow {
		t := template.Must(template.New("").Funcs(funcMap).ParseFiles(
			"templates/base.html",
			"templates/feature_row.html",
			"templates/group_action_btn.html",
			"templates/group_members_partial.html",
			"templates/feature_watch_btn.html",
			"templates/"+page,
		))
		m[page] = t
	}
	for _, page := range plain {
		t := template.Must(template.New("").Funcs(funcMap).ParseFiles(
			"templates/base.html",
			"templates/"+page,
		))
		m[page] = t
	}
	return m
}

// buildPartialTmpl parses templates used by HTMX-only endpoints.
func buildPartialTmpl() *template.Template {
	return template.Must(template.New("").Funcs(funcMap).ParseFiles(
		"templates/feature_row.html",
		"templates/features_partial.html",
		"templates/comments_partial.html",
		"templates/feature_detail.html",
		"templates/feature_watch_btn.html",
		"templates/stats_partial.html",
		"templates/submit_form_modal.html",
		"templates/notif_badge.html",
		"templates/notif_list.html",
		"templates/notif_read_response.html",
		"templates/message_badge.html",
		"templates/messages_preview.html",
		"templates/messages_center.html",
		"templates/preferences_partial.html",
		"templates/feature_draft_edit.html",
		"templates/feature_modify.html",
		"templates/group_action_btn.html",
		"templates/group_members_partial.html",
		"templates/notes_list_partial.html",
		"templates/notes_panel_partial.html",
		"templates/note_history.html",
	))
}

// buildIMTmpl parses the standalone IM window templates.
func buildIMTmpl() *template.Template {
	return template.Must(template.New("").Funcs(funcMap).ParseFiles(
		"templates/im_layout.html",
		"templates/im_sidebar.html",
		"templates/im_channel.html",
		"templates/im_notif_view.html",
		"templates/im_message_list.html",
		"templates/im_message_page.html",
		"templates/im_new_channel.html",
	))
}

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

	// Build JS bundle before templates so bundleVersion() returns correct hash.
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
	bundleVer = jsBundle.Version()
	rawSize, gzSize, brSize := jsBundle.Stats()
	log.Printf("js bundle: raw=%dB gzip=%dB brotli=%dB version=%s",
		rawSize, gzSize, brSize, bundleVer)

	// 笔记编辑器岛（esbuild 产物，构建方式见 web/notes-editor/）。
	// 复用 assets.Bundle 获得内容 hash + 预压缩；CSS 是手写的
	// static/css/notes-editor.css（Typora 式编辑页样式），走 /static/* 文件服务，
	// 单独按内容算 hash 作为 ?v= 参数。
	notesBundle, err := assets.NewBundle("static", []string{"notes-editor.js"})
	if err != nil {
		log.Fatal("bundle notes editor (先运行 web/notes-editor 下的 npm run build):", err)
	}
	notesEditorVer = notesBundle.Version()
	cssBytes, err := os.ReadFile("static/css/notes-editor.css")
	if err != nil {
		log.Fatal("read static/css/notes-editor.css:", err)
	}
	cssSum := sha256.Sum256(cssBytes)
	notesEditorCSSVer = hex.EncodeToString(cssSum[:6])
	nRaw, nGz, nBr := notesBundle.Stats()
	log.Printf("notes editor bundle: raw=%dB gzip=%dB brotli=%dB version=%s",
		nRaw, nGz, nBr, notesEditorVer)

	handler.SetTemplates(buildTmplMap(), buildPartialTmpl(), buildIMTmpl())

	// Brotli + gzip content-negotiating compression. Brotli wins for Chinese
	// users: ~20% smaller than gzip, which matters over 180ms RTT.
	brComp, err := brotlihttp.New(brotlihttp.Options{Quality: 5})
	if err != nil {
		log.Fatal("brotli init:", err)
	}
	compress, err := httpcompression.DefaultAdapter(
		httpcompression.Compressor(brotlihttp.Encoding, 1000, brComp),
	)
	if err != nil {
		log.Fatal("compression adapter:", err)
	}

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(compress)

	// JS bundle — pre-compressed at startup, served directly with
	// Content-Encoding already set so the middleware passes through.
	r.Get("/static/js/bundle.js", jsBundle.Handler())
	// 笔记编辑器 JS——精确路由优先于下面的 /static/* 通配
	r.Get("/static/notes-editor.js", notesBundle.Handler())

	// Static files with long cache (JS/CSS never change between deploys)
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		staticFS.ServeHTTP(w, r)
	}))

	// Public routes — default landing is /login
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	r.Get("/login", handler.LoginPage(database))
	r.Post("/login", handler.Login(database))
	r.Get("/register", handler.RegisterPage(database))
	r.Post("/register", handler.Register(database))
	r.Post("/logout", handler.Logout(database))

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return handler.RequireAuth(database, next)
		})

		r.Get("/dashboard", handler.Dashboard(database))

		r.Get("/sse", handler.SSE())
		r.Get("/stats", handler.GetStats(database))
		r.Get("/features/mine", handler.Mine(database))
		r.Get("/features/new", handler.FeatureForm(database))
		r.Get("/features/submit", handler.FeatureSubmitPage(database))
		r.Get("/features", handler.ListFeatures(database))
		r.Post("/features", handler.CreateFeature(database))
		r.Get("/features/{id}", handler.FeatureDetail(database))
		r.Get("/features/{id}/edit", handler.DraftEditForm(database))
		r.Post("/features/{id}/edit", handler.UpdateDraft(database))
		r.Get("/features/{id}/modify", handler.ModifyContentForm(database))
		r.Post("/features/{id}/modify", handler.UpdateFeatureContent(database))
		r.Get("/features/{id}/row", handler.GetFeatureRow(database))
		r.Get("/features/{id}/comments", handler.GetComments(database))
		r.Delete("/features/{id}", handler.RetractFeature(database))
		r.Patch("/features/{id}/status", handler.UpdateStatus(database))
		r.Post("/features/{id}/archive", handler.ArchiveFeature(database))
		r.Post("/features/{id}/comments", handler.AddComment(database))
		r.Delete("/features/{id}/comments/{commentID}", handler.DeleteComment(database))
		r.Post("/features/{id}/watch", handler.WatchFeature(database))
		r.Delete("/features/{id}/watch", handler.UnwatchFeature(database))

		r.Post("/uploads/image", handler.UploadImage(database))
		r.Get("/uploads/{id}/{variant}", handler.ServeUpload(database))

		r.Get("/notifications/count", handler.GetNotificationBadge(database))
		r.Get("/notifications", handler.GetNotificationList(database))
		r.Post("/notifications/read", handler.MarkNotificationsRead(database))
		r.Post("/notifications/read-all", handler.MarkAllNotificationsRead(database))
		r.Get("/messages/count", handler.GetMessageBadge(database))
		r.Get("/messages/preview", handler.GetMessagePreview(database))
		r.Get("/messages/center", handler.GetMessageCenter(database))
		r.Post("/messages/send", handler.SendMessage(database))

		r.Get("/preferences", handler.Preferences(database))

		// IM routes
		r.Get("/im", handler.IMHome(database))
		r.Get("/im/sidebar", handler.IMSidebar(database))
		r.Get("/im/notifications", handler.IMNotifications(database))
		r.Get("/im/channels/new", handler.IMNewChannelForm(database))
		r.Post("/im/channels", handler.CreateIMChannel(database))
		r.Get("/im/c/{id}", handler.IMChannel(database))
		r.Get("/im/c/{id}/messages", handler.GetIMMessages(database))
		r.Get("/im/c/{id}/messages/new", handler.GetNewIMMessages(database))
		r.Post("/im/c/{id}/messages", handler.SendIMMessage(database))
		r.Post("/im/c/{id}/join", handler.JoinIMChannel(database))
		r.Delete("/im/c/{id}/join", handler.LeaveIMChannel(database))
		// DM routes — read/write direct_messages table (同 Tasks 消息中心)
		r.Get("/im/dm/{userID}", handler.IMDMView(database))
		r.Post("/im/dm/{userID}/messages", handler.SendIMDM(database))
		r.Get("/im/dm/{userID}/messages/new", handler.GetNewIMDMMessages(database))
		// WebRTC signaling relay
		r.Post("/im/call/signal", handler.CallSignal(database))

		// 云笔记
		r.Get("/notes", handler.NotesPage(database))
		r.Get("/notes/new", handler.NewNote(database))
		r.Get("/notes/panel", handler.NotesPanel(database)) // 编辑页右侧面板（chi 静态段优先于 {id}）
		r.Get("/notes/{id}", handler.NoteEditPage(database))
		r.Put("/notes/{id}", handler.SaveNote(database))
		r.Delete("/notes/{id}", handler.DeleteNote(database))
		r.Get("/notes/{id}/history", handler.NoteHistory(database))
		r.Get("/notes/{id}/revisions/{rid}", handler.NoteRevisionPage(database))
		r.Post("/notes/{id}/restore/{rid}", handler.RestoreNoteRevision(database))
		r.Post("/notes/{id}/attachments", handler.UploadNoteAttachment(database))
		r.Get("/attachments/{id}/{name}", handler.ServeNoteAttachment(database))

		r.Get("/groups", handler.ListGroups(database))
		r.Post("/groups", handler.CreateGroup(database))
		r.Get("/groups/{id}", handler.GroupDetail(database))
		r.Post("/groups/{id}/join", handler.JoinGroup(database))
		r.Delete("/groups/{id}/join", handler.LeaveGroup(database))
		r.Post("/groups/{id}/watch", handler.WatchGroup(database))
		r.Delete("/groups/{id}/watch", handler.UnwatchGroup(database))
		r.Post("/groups/{id}/members", handler.AddGroupMember(database))
		r.Delete("/groups/{id}/members/{uid}", handler.RemoveGroupMember(database))
	})

	// 自动归档：每小时扫描一次，将 done 超过 24h 的功能归档
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			archived, err := database.AutoArchiveFeatures()
			if err != nil {
				log.Println("auto-archive error:", err)
			} else if len(archived) > 0 {
				seen := map[int64]bool{}
				for _, f := range archived {
					if err := database.CreateNotification(&model.Notification{
						UserID:       f.CreatedBy,
						FeatureID:    f.ID,
						FromUser:     "系统",
						FeatureTitle: f.Title,
						Message:      model.FeatureStatusNotificationText(f.Title, "archived", true),
					}); err != nil {
						log.Printf("auto-archive notification error for feature %d: %v", f.ID, err)
						continue
					}
					if !seen[f.CreatedBy] {
						seen[f.CreatedBy] = true
						hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", f.CreatedBy))
					}
				}
				log.Printf("auto-archived %d feature(s)", len(archived))
				hub.Global.Broadcast("feature-list-changed")
				hub.Global.Broadcast("stats-updated")
			}
		}
	}()

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" { // 本地开发可覆盖端口，默认行为不变
		addr = ":" + p
	}
	log.Println("Listening on", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second, // keep-alive for high-latency VPN
	}
	log.Fatal(srv.ListenAndServe())
}

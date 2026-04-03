package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"featuretrack/internal/db"
	"featuretrack/internal/handler"
	"featuretrack/internal/hub"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

var mentionHighlightRe = regexp.MustCompile(`@([\p{L}\p{N}_]+)`)

var funcMap = template.FuncMap{
	"add": func(a, b int) int { return a + b },
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
	plain := []string{"login.html", "register.html", "groups.html", "submit_standalone.html"}

	m := make(map[string]*template.Template)
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

	handler.SetTemplates(buildTmplMap(), buildPartialTmpl())

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5)) // gzip — saves ~70% on HTML/CSS transfers

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
		r.Post("/features/{id}/watch", handler.WatchFeature(database))
		r.Delete("/features/{id}/watch", handler.UnwatchFeature(database))

		r.Get("/notifications/count", handler.GetNotificationBadge(database))
		r.Get("/notifications", handler.GetNotificationList(database))
		r.Post("/notifications/read", handler.MarkNotificationsRead(database))
		r.Post("/notifications/read-all", handler.MarkAllNotificationsRead(database))
		r.Get("/messages/count", handler.GetMessageBadge(database))
		r.Get("/messages/preview", handler.GetMessagePreview(database))
		r.Get("/messages/center", handler.GetMessageCenter(database))
		r.Post("/messages/send", handler.SendMessage(database))

		r.Get("/preferences", handler.Preferences(database))

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
	log.Println("Listening on", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second, // keep-alive for high-latency VPN
	}
	log.Fatal(srv.ListenAndServe())
}

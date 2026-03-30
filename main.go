package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"featuretrack/internal/db"
	"featuretrack/internal/handler"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

var funcMap = template.FuncMap{
	"add": func(a, b int) int { return a + b },
}

// buildTmplMap parses one isolated template set per full-page template.
// This prevents {{define "content"}} from colliding across pages.
func buildTmplMap() map[string]*template.Template {
	// Pages that extend base.html and need feature_row partial
	withRow := []string{"dashboard.html", "mine.html", "group_detail.html"}
	// Pages that extend base.html, no partials needed
	plain := []string{"login.html", "register.html", "groups.html"}

	m := make(map[string]*template.Template)
	for _, page := range withRow {
		t := template.Must(template.New("").Funcs(funcMap).ParseFiles(
			"templates/base.html",
			"templates/feature_row.html",
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
		"templates/stats_partial.html",
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
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

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
		r.Get("/features", handler.ListFeatures(database))
		r.Post("/features", handler.CreateFeature(database))
		r.Get("/features/{id}", handler.FeatureDetail(database))
		r.Get("/features/{id}/row", handler.GetFeatureRow(database))
		r.Get("/features/{id}/comments", handler.GetComments(database))
		r.Delete("/features/{id}", handler.RetractFeature(database))
		r.Patch("/features/{id}/status", handler.UpdateStatus(database))
		r.Post("/features/{id}/comments", handler.AddComment(database))

		r.Get("/groups", handler.ListGroups(database))
		r.Post("/groups", handler.CreateGroup(database))
		r.Get("/groups/{id}", handler.GroupDetail(database))
	})

	addr := ":8080"
	log.Println("Listening on", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}

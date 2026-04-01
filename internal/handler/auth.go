package handler

import (
	"context"
	"html/template"
	"net/http"
	"regexp"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/model"
	"featuretrack/internal/session"

	"golang.org/x/crypto/bcrypt"
)

// usernameRe validates that a username contains only letters, digits, and underscores (no spaces).
var usernameRe = regexp.MustCompile(`^[\p{L}\p{N}_]+$`)

// ── shared template helper ─────────────────────────────────────────────────

// tmplMap holds one isolated template set per full page (avoids {{define "content"}} collision).
var tmplMap map[string]*template.Template

// PartialTmpl is used by HTMX-only endpoints (feature list, row swap).
var PartialTmpl *template.Template

func SetTemplates(m map[string]*template.Template, p *template.Template) {
	tmplMap = m
	PartialTmpl = p
}

func render(w http.ResponseWriter, r *http.Request, name string, data *model.PageData) {
	t, ok := tmplMap[name]
	if !ok {
		http.Error(w, "template not found: "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, url string) {
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// ── middleware ─────────────────────────────────────────────────────────────

type contextKey string

const ctxUser contextKey = "user"

func RequireAuth(database *db.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session.GetUser(r, database)
		if err != nil || u == nil {
			redirect(w, r, "/login")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserFromContext(r *http.Request) *model.User {
	u, _ := r.Context().Value(ctxUser).(*model.User)
	return u
}

// ── page base data ─────────────────────────────────────────────────────────

func pageData(r *http.Request, activeNav string) *model.PageData {
	u := UserFromContext(r)
	return &model.PageData{
		CurrentUser: u,
		ActiveNav:   activeNav,
	}
}

func withFlash(pd *model.PageData, t, msg string) *model.PageData {
	pd.Flash = &model.Flash{Type: t, Message: msg}
	return pd
}

// ── handlers ──────────────────────────────────────────────────────────────

func LoginPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, _ := session.GetUser(r, database); u != nil {
			redirect(w, r, "/dashboard")
			return
		}
		render(w, r, "login.html", &model.PageData{})
	}
}

func Login(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")

		u, err := database.GetUserByUsername(username)
		if err != nil {
			render(w, r, "login.html", withFlash(&model.PageData{}, "error", "服务器错误，请重试"))
			return
		}
		if u == nil || bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
			pd := &model.PageData{}
			render(w, r, "login.html", withFlash(pd, "error", "用户名或密码错误"))
			return
		}
		if err := session.Set(w, r, database, u.ID); err != nil {
			render(w, r, "login.html", withFlash(&model.PageData{}, "error", "登录失败，请重试"))
			return
		}
		redirect(w, r, "/dashboard")
	}
}

func RegisterPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, _ := session.GetUser(r, database); u != nil {
			redirect(w, r, "/dashboard")
			return
		}
		render(w, r, "register.html", &model.PageData{})
	}
}

func Register(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.FormValue("username"))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		email := strings.TrimSpace(r.FormValue("email"))
		password := r.FormValue("password")
		role := r.FormValue("role")

		if username == "" || email == "" || password == "" {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "请填写所有必填项"))
			return
		}
		if !usernameRe.MatchString(username) {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "用户名只能包含字母、数字和下划线，不能含有空格"))
			return
		}
		if len(password) < 6 {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "密码至少需要6位"))
			return
		}
		if role != "pm" && role != "dev" {
			role = "pm"
		}
		if displayName == "" {
			displayName = username
		}

		exists, _ := database.UsernameExists(username)
		if exists {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "用户名已被占用"))
			return
		}
		eExists, _ := database.EmailExists(email)
		if eExists {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "邮箱已被注册"))
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "服务器错误，请重试"))
			return
		}
		u := &model.User{Username: username, DisplayName: displayName, Email: email, Password: string(hash), Role: role}
		if err := database.CreateUser(u); err != nil {
			render(w, r, "register.html", withFlash(&model.PageData{}, "error", "注册失败，请重试"))
			return
		}
		if err := session.Set(w, r, database, u.ID); err != nil {
			redirect(w, r, "/login")
			return
		}
		redirect(w, r, "/dashboard")
	}
}

func Logout(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session.Delete(w, r, database)
		redirect(w, r, "/login")
	}
}

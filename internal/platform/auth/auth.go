// Package auth 承载登录态与用户上下文:RequireAuth 中间件、UserFromContext、
// PageData 基础数据,以及登录/注册/退出三组端点(自带 login/register 模板集)。
package auth

import (
	"context"
	"html/template"
	"net/http"
	"regexp"
	"strings"

	"visualink/internal/db"
	"visualink/internal/model"
	"visualink/internal/platform/session"
	"visualink/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

// usernameRe 校验用户名只含字母/数字/下划线(无空格)。
var usernameRe = regexp.MustCompile(`^[\p{L}\p{N}_]+$`)

var pages map[string]*template.Template

// InitTemplates 解析本模块的页面模板集(登录/注册,套 base.html)。
func InitTemplates() {
	pages = map[string]*template.Template{
		"login.html":    web.ParseSet("base.html", "login.html"),
		"register.html": web.ParseSet("base.html", "register.html"),
	}
}

// ── 中间件与用户上下文 ──────────────────────────────────────────────────

type contextKey string

const ctxUser contextKey = "user"

func RequireAuth(database *db.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := session.GetUser(r, database)
		if err != nil || u == nil {
			web.Redirect(w, r, "/login")
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

// ── 页面基础数据 ────────────────────────────────────────────────────────

func PageData(r *http.Request, activeNav string) *model.PageData {
	return &model.PageData{
		CurrentUser: UserFromContext(r),
		ActiveNav:   activeNav,
	}
}

func WithFlash(pd *model.PageData, t, msg string) *model.PageData {
	pd.Flash = &model.Flash{Type: t, Message: msg}
	return pd
}

// ── 路由 ────────────────────────────────────────────────────────────────

// Routes 注册公开路由(登录/注册/退出)。
func Routes(r chi.Router, database *db.DB) {
	r.Get("/login", loginPage(database))
	r.Post("/login", login(database))
	r.Get("/register", registerPage(database))
	r.Post("/register", register(database))
	r.Post("/logout", logout(database))
}

// ── 端点 ────────────────────────────────────────────────────────────────

func render(w http.ResponseWriter, name string, data *model.PageData) {
	web.RenderPage(w, pages, name, data)
}

func loginPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, _ := session.GetUser(r, database); u != nil {
			web.Redirect(w, r, "/dashboard")
			return
		}
		render(w, "login.html", &model.PageData{})
	}
}

func login(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")

		u, err := database.GetUserByUsername(username)
		if err != nil {
			render(w, "login.html", WithFlash(&model.PageData{}, "error", "服务器错误，请重试"))
			return
		}
		if u == nil || bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
			render(w, "login.html", WithFlash(&model.PageData{}, "error", "用户名或密码错误"))
			return
		}
		if err := session.Set(w, r, database, u.ID); err != nil {
			render(w, "login.html", WithFlash(&model.PageData{}, "error", "登录失败，请重试"))
			return
		}
		web.Redirect(w, r, "/dashboard")
	}
}

func registerPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, _ := session.GetUser(r, database); u != nil {
			web.Redirect(w, r, "/dashboard")
			return
		}
		render(w, "register.html", &model.PageData{})
	}
}

func register(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.FormValue("username"))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		email := strings.TrimSpace(r.FormValue("email"))
		password := r.FormValue("password")
		role := r.FormValue("role")

		if username == "" || email == "" || password == "" {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "请填写所有必填项"))
			return
		}
		if !usernameRe.MatchString(username) {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "用户名只能包含字母、数字和下划线，不能含有空格"))
			return
		}
		if len(password) < 6 {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "密码至少需要6位"))
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
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "用户名已被占用"))
			return
		}
		eExists, _ := database.EmailExists(email)
		if eExists {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "邮箱已被注册"))
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "服务器错误，请重试"))
			return
		}
		u := &model.User{Username: username, DisplayName: displayName, Email: email, Password: string(hash), Role: role}
		if err := database.CreateUser(u); err != nil {
			render(w, "register.html", WithFlash(&model.PageData{}, "error", "注册失败，请重试"))
			return
		}
		if err := session.Set(w, r, database, u.ID); err != nil {
			web.Redirect(w, r, "/login")
			return
		}
		web.Redirect(w, r, "/dashboard")
	}
}

func logout(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session.Delete(w, r, database)
		web.Redirect(w, r, "/login")
	}
}

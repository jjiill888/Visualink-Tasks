// Package web 是全系统共享的模板渲染基座:模板函数表、资产版本号、
// 模板集解析与三种渲染方式(base 壳页/独立整页/片段)。
// 各业务模块自建模板集(谁的页面谁解析),本包只提供原语——
// buildPartialTmpl 时代「十个域一锅粥」的模板集合不再存在。
package web

import (
	"html/template"
	"net/http"
	"regexp"
	"strings"
)

// ── 资产版本状态(启动时由 main 注入,模板函数经闭包读取) ──────────────

// Assets 汇集模板需要的资产版本号与内联内容,main 构建资产后一次性写入。
type Assets struct {
	BundleVer         string       // 主 JS bundle 内容 hash
	NotesEditorVer    string       // 笔记编辑器 JS 内容 hash
	NotesEditorCSSVer string       // 笔记编辑器 CSS 内容 hash
	NeBaseCSS         template.CSS // 设计词元 ne-base.css 全文(内联进 <head>)
	NotesEditorChunks []string     // esbuild ESM 分割 chunk 文件名(modulepreload)
}

var assets Assets

// SetAssets 注入资产版本;必须先于任何模板解析调用(FuncMap 闭包捕获的是
// 包级变量,解析本身不读值,执行时才读,故顺序上只需早于首次渲染)。
func SetAssets(a Assets) { assets = a }

var mentionHighlightRe = regexp.MustCompile(`@([\p{L}\p{N}_]+)`)

// FuncMap 返回全系统共享的模板函数表。
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"add":                   func(a, b int) int { return a + b },
		"bundleVersion":         func() string { return assets.BundleVer },
		"notesEditorVersion":    func() string { return assets.NotesEditorVer },
		"notesEditorCSSVersion": func() string { return assets.NotesEditorCSSVer },
		"neBaseCSS":             func() template.CSS { return assets.NeBaseCSS },
		"notesEditorChunks":     func() []string { return assets.NotesEditorChunks },
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
}

// ── 模板集解析 ──────────────────────────────────────────────────────────

// ParseSet 解析一个隔离模板集(文件名相对 templates/)。每个整页一个隔离集,
// 防止 {{define "content"}} 跨页冲突——原 buildTmplMap 的不变量保持不变。
func ParseSet(files ...string) *template.Template {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = "templates/" + f
	}
	return template.Must(template.New("").Funcs(FuncMap()).ParseFiles(paths...))
}

// ── 渲染原语 ────────────────────────────────────────────────────────────

// RenderPage 渲染套 base.html 外壳的整页:从模块自己的页面集查 name。
func RenderPage(w http.ResponseWriter, pages map[string]*template.Template, name string, data any) {
	t, ok := pages[name]
	if !ok {
		http.Error(w, "template not found: "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// RenderStandalone 渲染不套 base.html 的独立整页(如笔记编辑页),
// 模板以自身文件名为入口。
func RenderStandalone(w http.ResponseWriter, pages map[string]*template.Template, name string, data any) {
	t, ok := pages[name]
	if !ok {
		http.Error(w, "template not found: "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// Redirect 统一 303 跳转(POST 后重定向语义)。
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	http.Redirect(w, r, url, http.StatusSeeOther)
}

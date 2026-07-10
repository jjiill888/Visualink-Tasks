package handler

// 云笔记阶段二：y-sweet 实时协作的服务端胶水。
//
// 架构（y-sweet 只在 compose 内网，浏览器一律经 Go 走）：
//
//	浏览器 ──(带 session cookie)── Go ──(内网)── y-sweet
//	  ①  GET /notes/{id}/collab-token   权限校验后向 y-sweet 签发房间 token
//	  ②  WS  /collab/d/{doc}/ws/...     纯反代（y-sweet 自己校验 token，实测
//	                                     伪 token 返回 401；反代挂在登录中间件后，
//	                                     未登录连 token 都拿不到，双层拦截）
//
// 房间命名：文档 "note-{id}" 对应 notes 表一行。Markdown 快照回写仍走现有
// PUT /notes/{id}（前端静默 3 秒触发，base_updated_at 传空串跳过乐观锁），
// FTS / 历史版本机制完全不变。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"featuretrack/internal/db"
)

// CollabUpstream y-sweet 服务地址（compose 内网，如 http://ysweet:8080）。
// 为空 = 协作功能关闭，编辑页维持阶段一单人模式，前端不加载任何协作逻辑。
var CollabUpstream = strings.TrimRight(os.Getenv("Y_SWEET_URL"), "/")

// collabServerToken y-sweet API 的 server token（gen-auth --json 的 server_token，
// 不是 private_key——private key 给 y-sweet 的 --auth，两者不通用，实测串用 401）。
var collabServerToken = os.Getenv("Y_SWEET_AUTH")

// CollabEnabled 供模板决定是否给编辑器岛置 data-collab 标志。
func CollabEnabled() bool { return CollabUpstream != "" }

// collabHTTP 调 y-sweet API 的短超时客户端（内网调用，不该慢）。
var collabHTTP = &http.Client{Timeout: 5 * time.Second}

// ysweetPost 带 server token 调 y-sweet 管理 API。
func ysweetPost(path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, CollabUpstream+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if collabServerToken != "" {
		req.Header.Set("Authorization", "Bearer "+collabServerToken)
	}
	return collabHTTP.Do(req)
}

type collabClientToken struct {
	URL           string `json:"url"`
	BaseURL       string `json:"baseUrl"`
	DocID         string `json:"docId"`
	Token         string `json:"token,omitempty"`
	Authorization string `json:"authorization,omitempty"`
}

// rewriteCollabURL 把 y-sweet 返回的内网地址改写成浏览器可达的 /collab 反代地址。
// wsScheme=true 输出 ws(s)，否则 http(s)；是否 TLS 看请求本身（生产纯 HTTP 直出）。
func rewriteCollabURL(r *http.Request, upstream string, wsScheme bool) string {
	u, err := url.Parse(upstream)
	if err != nil {
		return upstream
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	scheme := "http"
	if wsScheme {
		scheme = "ws"
	}
	if secure {
		scheme += "s"
	}
	return fmt.Sprintf("%s://%s/collab%s", scheme, r.Host, u.Path)
}

// CollabToken GET /notes/{id}/collab-token — 校验笔记权限后签发 y-sweet 房间 token。
// 返回给浏览器的 ClientToken 里，内网 ws 地址已改写为本服务的 /collab 反代路径
// （按请求的 Host 和协议动态拼，生产走 VPN 直连 IP、开发走 localhost 都不用配置）。
func CollabToken(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !CollabEnabled() {
			http.Error(w, "协作服务未启用", http.StatusServiceUnavailable)
			return
		}
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		docID := fmt.Sprintf("note-%d", n.ID)

		// /doc/new 幂等：文档已存在时同样 200，无需先查再建
		resp, err := ysweetPost("/doc/new", map[string]string{"docId": docID})
		if err != nil {
			http.Error(w, "协作服务不可用", http.StatusBadGateway)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "协作服务拒绝创建房间", http.StatusBadGateway)
			return
		}

		resp, err = ysweetPost("/doc/"+docID+"/auth", map[string]string{"authorization": "full"})
		if err != nil {
			http.Error(w, "协作服务不可用", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "协作服务签发 token 失败", http.StatusBadGateway)
			return
		}
		var tok collabClientToken
		if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
			http.Error(w, "协作服务响应异常", http.StatusBadGateway)
			return
		}
		tok.URL = rewriteCollabURL(r, tok.URL, true)
		tok.BaseURL = rewriteCollabURL(r, tok.BaseURL, false)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store") // token 短效凭据，禁止任何缓存
		json.NewEncoder(w).Encode(tok)
	}
}

// CollabProxy /collab/* 反代到 y-sweet（含 websocket——httputil.ReverseProxy
// 自 Go 1.12 起原生处理 Upgrade）。挂在登录中间件后：未登录 401，
// 已登录但拿着伪 token 由 y-sweet 拒绝（实测 401），双层拦截。
func CollabProxy() http.Handler {
	target, err := url.Parse(CollabUpstream)
	if err != nil || CollabUpstream == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "协作服务未启用", http.StatusServiceUnavailable)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		// 去掉 /collab 前缀还原 y-sweet 原生路径（如 /d/note-1/ws/note-1）
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/collab")
		req.URL.RawPath = ""
	}
	return proxy
}

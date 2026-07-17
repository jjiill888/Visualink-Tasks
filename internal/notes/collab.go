package notes

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
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"visualink/internal/db"
)

// CollabUpstream y-sweet 服务地址（compose 内网，如 http://ysweet:8080）。
// 为空 = 协作功能关闭，编辑页维持阶段一单人模式，前端不加载任何协作逻辑。
var CollabUpstream = strings.TrimRight(os.Getenv("Y_SWEET_URL"), "/")

// collabServerToken y-sweet API 的 server token（gen-auth --json 的 server_token，
// 不是 private_key——private key 给 y-sweet 的 --auth，两者不通用，实测串用 401）。
var collabServerToken = os.Getenv("Y_SWEET_AUTH")

// CollabPublicPort 可信内网直连模式（性能优先，用户明确授权安全让步）：
// 设置后签发的 ws 地址直指 y-sweet 对宿主机暴露的端口（compose ports 映射），
// 浏览器绕过 Go 反代直连，数据路径少一跳。裸连仍需有效房间 token（y-sweet
// 自己校验，伪 token 401），只是少了登录 session 这一层。不设 = 走 /collab 反代。
var CollabPublicPort = os.Getenv("Y_SWEET_PUBLIC_PORT")

// CollabEnabled 供模板决定是否给编辑器岛置 data-collab 标志。
func CollabEnabled() bool { return CollabUpstream != "" }

// collabHTTP 调 y-sweet API 的客户端（token 端点用，可容忍慢）。
var collabHTTP = &http.Client{Timeout: 5 * time.Second}

// collabPageHTTP 页面渲染路径内联 token 用的短超时客户端：
// y-sweet 挂掉时绝不能拖慢编辑页首屏，超时后由前端走降级流程。
var collabPageHTTP = &http.Client{Timeout: 1200 * time.Millisecond}

// collabDocKnown 已确认在 y-sweet 建过房的笔记（/doc/new 幂等但仍是一次内网
// 往返，首次之后跳过——签 token 稳态只剩一次 API 调用）。
var collabDocKnown sync.Map

// ysweetPost 带 server token 调 y-sweet 管理 API。
func ysweetPost(client *http.Client, path string, body any) (*http.Response, error) {
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
	return client.Do(req)
}

type collabClientToken struct {
	URL           string `json:"url"`
	BaseURL       string `json:"baseUrl"`
	DocID         string `json:"docId"`
	Token         string `json:"token,omitempty"`
	Authorization string `json:"authorization,omitempty"`
}

// rewriteCollabURL 把 y-sweet 返回的内网地址改写成浏览器可达的地址：
//   - 直连模式（Y_SWEET_PUBLIC_PORT）：同一主机名 + 公开端口，路径原样——
//     浏览器怎么访问到本服务，就用同一个主机名连 y-sweet，ZeroTier IP /
//     localhost 都无需配置
//   - 反代模式：本服务的 /collab 前缀路径
//
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
	if CollabPublicPort != "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		return fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(host, CollabPublicPort), u.Path)
	}
	return fmt.Sprintf("%s://%s/collab%s", scheme, r.Host, u.Path)
}

// fetchCollabToken 建房（带缓存）并签发房间 token，地址改写为浏览器可达形态。
// client 决定超时档位：页面渲染路径用短超时，token 端点用标准超时。
func fetchCollabToken(r *http.Request, noteID int64, client *http.Client) (*collabClientToken, error) {
	docID := fmt.Sprintf("note-%d", noteID)
	if _, ok := collabDocKnown.Load(noteID); !ok {
		resp, err := ysweetPost(client, "/doc/new", map[string]string{"docId": docID})
		if err != nil {
			return nil, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("doc/new status %d", resp.StatusCode)
		}
		collabDocKnown.Store(noteID, true)
	}
	// validForSeconds 12 小时：重连直接复用 initialClientToken，不再回源取号
	resp, err := ysweetPost(client, "/doc/"+docID+"/auth",
		map[string]any{"authorization": "full", "validForSeconds": 43200})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth status %d", resp.StatusCode)
	}
	var tok collabClientToken
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	tok.URL = rewriteCollabURL(r, tok.URL, true)
	tok.BaseURL = rewriteCollabURL(r, tok.BaseURL, false)
	return &tok, nil
}

// InlineCollabToken 编辑页渲染时预签 token 直出到模板——省掉前端一次
// token round-trip，ws 在 JS 解析完立即可连（跨境 ZeroTier 一个 RTT 不便宜）。
// 失败返回空串：页面不因 y-sweet 故障变慢，前端回退到 token 端点→降级链路。
func InlineCollabToken(r *http.Request, noteID int64) string {
	if !CollabEnabled() {
		return ""
	}
	tok, err := fetchCollabToken(r, noteID, collabPageHTTP)
	if err != nil {
		return ""
	}
	buf, err := json.Marshal(tok)
	if err != nil {
		return ""
	}
	return string(buf)
}

// CollabToken GET /notes/{id}/collab-token — 校验编辑权后签发 y-sweet 房间 token
// （受限笔记的 reader 拿不到 token，进不了协作房间）。
// 返回给浏览器的 ClientToken 里，内网 ws 地址已改写为本服务的 /collab 反代路径
// （按请求的 Host 和协议动态拼，生产走 VPN 直连 IP、开发走 localhost 都不用配置）。
func CollabToken(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !CollabEnabled() {
			http.Error(w, "协作服务未启用", http.StatusServiceUnavailable)
			return
		}
		n := loadNoteForEdit(w, r, database)
		if n == nil {
			return
		}
		tok, err := fetchCollabToken(r, n.ID, collabHTTP)
		if err != nil {
			http.Error(w, "协作服务不可用", http.StatusBadGateway)
			return
		}
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

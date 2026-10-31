package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// rewriteToUpstreamTransport 把白名单域名的请求在传输层改写到 httptest 假
// 上游：媒体代理的 URL 准入认 GitHub 域名（normalize 层），注入的客户端
// 只负责让这些请求落到本地假上游，两层各管各的。hits 统计真正打到上游的
// 请求数，验证缓存命中不再重复拉取。
type rewriteToUpstreamTransport struct {
	target *url.URL
	hits   atomic.Int32
}

func (t *rewriteToUpstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.hits.Add(1)
	rewritten := req.Clone(req.Context())
	rewritten.URL.Scheme = t.target.Scheme
	rewritten.URL.Host = t.target.Host
	rewritten.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

// INT-171：handler 直测媒体代理的 svg 闸门与硬化响应头——不经 DB 与鉴权
// 中间件，gin.New() 直接注册 handler；上游由注入客户端指向的 httptest
// 假上游扮演。
func TestGitHubMediaProxyHandlerSVGGateAndHardeningHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user-attachments/assets/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-bytes"))
		case "/user-attachments/assets/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-3/8")
				w.WriteHeader(http.StatusPartialContent)
			}
			_, _ = w.Write([]byte("mp4-bytes"))
		case "/user-attachments/assets/evil.html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<p>html</p>"))
		default:
			// 其余路径一律当 svg 上游，覆盖 Content-Type 形态与 HEAD/Range 请求。
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg onload="alert(1)"></svg>`))
		}
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	upstreamTransport := &rewriteToUpstreamTransport{target: target}
	svc := githubmedia.NewProxyService(t.TempDir(),
		githubmedia.WithHTTPTransport(upstreamTransport))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	proxy := NewGitHubMediaProxyHandler(svc).Proxy
	router.GET(githubmedia.ProxyPath, proxy)
	router.HEAD(githubmedia.ProxyPath, proxy)

	assertHardening := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		for key, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"Content-Disposition":     "attachment",
			"Content-Security-Policy": "default-src 'none'; sandbox",
		} {
			if got := rec.Header().Get(key); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
		}
	}

	// svg 上游：整文件、HEAD、Range 一律 415。
	for _, tc := range []struct {
		name       string
		method     string
		rangeBound string
	}{
		{"full get", http.MethodGet, ""},
		{"head", http.MethodHead, ""},
		{"range", http.MethodGet, "bytes=0-3"},
	} {
		t.Run("svg "+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/evil.svg"), nil)
			if tc.rangeBound != "" {
				req.Header.Set("Range", tc.rangeBound)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("code = %d body = %s, want 415", rec.Code, rec.Body.String())
			}
		})
	}

	// text/html 上游同拒。
	req := httptest.NewRequest(http.MethodGet, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/evil.html"), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("html code = %d, want 415", rec.Code)
	}

	// png 整文件走缓存命中：200 + 三个硬化头；显式设置的 Content-Type
	// 不被 http.ServeContent 覆盖。
	req = httptest.NewRequest(http.MethodGet, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/ok.png"), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("png code = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("png content type = %q, want image/png（不被 ServeContent 覆盖）", got)
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("png body = %q, want png-bytes", rec.Body.String())
	}
	assertHardening(t, rec)

	// mp4 的 Range 仍 206 正常透传：上游 Content-Range 原样返回 + 三个硬化头。
	req = httptest.NewRequest(http.MethodGet, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/clip.mp4"), nil)
	req.Header.Set("Range", "bytes=0-3")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("mp4 range code = %d, want 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/8" {
		t.Fatalf("mp4 content range = %q, want bytes 0-3/8", got)
	}
	assertHardening(t, rec)

	// 成功的 HEAD（ProxyPass 直通路径）同样带三个硬化头。
	req = httptest.NewRequest(http.MethodHead, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/clip.mp4"), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mp4 head code = %d, want 200", rec.Code)
	}
	assertHardening(t, rec)

	// 同一 .png 连续两次 GET：第二次走缓存命中不再打上游，硬化头照带。
	// 此前已经 GET 过一次 ok.png（首次下载入缓存）。
	hitsBefore := upstreamTransport.hits.Load()
	req = httptest.NewRequest(http.MethodGet, githubmedia.ProxyPath+"?url="+url.QueryEscape("http://github.com/user-attachments/assets/ok.png"), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cached png code = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("cached png body = %q, want png-bytes", rec.Body.String())
	}
	if got := upstreamTransport.hits.Load(); got != hitsBefore {
		t.Fatalf("upstream hits = %d, want %d（缓存命中不再打上游）", got, hitsBefore)
	}
	assertHardening(t, rec)

	// 多段 Range（逗号分隔）被忽略，按无 Range 走整文件路径：返回 200 完整
	// body 而不是 415——上游对多段 Range 回 multipart/byteranges，闸门必然
	// 拒，RFC 7233 允许忽略 Range。先整文件 GET 预热进缓存，多段请求才落在
	// 缓存命中路径：那条路的 http.ServeContent 会自己再解析 Range，不删
	// 请求头就会变回 206 multipart。
	mp4URL := githubmedia.ProxyPath + "?url=" + url.QueryEscape("http://github.com/user-attachments/assets/clip.mp4")
	req = httptest.NewRequest(http.MethodGet, mp4URL, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "mp4-bytes" {
		t.Fatalf("mp4 preheat code = %d body = %q, want 200 完整内容", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, mp4URL, nil)
	req.Header.Set("Range", "bytes=0-1,4-5")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi-range code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "mp4-bytes" {
		t.Fatalf("multi-range body = %q, want mp4-bytes（完整内容）", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("multi-range content-range = %q, want 空（未按 Range 切片）", got)
	}
	assertHardening(t, rec)
}

// 错误映射单测：415 / 400 / 上游状态透传与 5xx 折 502 的口径不变。
func TestMediaProxyErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid url", githubmedia.ErrInvalidMediaURL, http.StatusBadRequest},
		{"unsupported upstream media", githubmedia.ErrUnsupportedUpstreamMedia, http.StatusUnsupportedMediaType},
		{"upstream 404 passthrough", &githubmedia.UpstreamStatusError{StatusCode: http.StatusNotFound}, http.StatusNotFound},
		{"upstream 5xx folded to 502", &githubmedia.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable}, http.StatusBadGateway},
		{"opaque failure", errors.New("boom"), http.StatusBadGateway},
	}
	gin.SetMode(gin.TestMode)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			writeMediaProxyError(c, tc.err)
			if c.Writer.Status() != tc.want {
				t.Fatalf("status = %d, want %d", c.Writer.Status(), tc.want)
			}
		})
	}
}

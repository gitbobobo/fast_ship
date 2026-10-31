package githubmedia

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRewriteHTMLMediaSources(t *testing.T) {
	input := `<p><img src="https://github.com/user-attachments/assets/demo.png" alt="demo"></p><video poster="https://avatars.githubusercontent.com/u/1"><source src="https://user-images.githubusercontent.com/demo.mp4" type="video/mp4"></video>`
	output := RewriteHTMLMediaSources(input)

	if !strings.Contains(output, ProxyPath+"?url=") {
		t.Fatalf("expected proxied media urls, got %q", output)
	}
	if !strings.Contains(output, url.QueryEscape("https://github.com/user-attachments/assets/demo.png")) {
		t.Fatalf("expected img url to be rewritten, got %q", output)
	}
	if !strings.Contains(output, url.QueryEscape("https://user-images.githubusercontent.com/demo.mp4")) {
		t.Fatalf("expected source url to be rewritten, got %q", output)
	}
}

func TestNormalizeMediaURL_AllowsGitHubUserAttachments(t *testing.T) {
	value := "https://github.com/user-attachments/assets/0f3a93f0-dd2c-416b-8638-77d85b0a2c07"

	normalized, err := normalizeMediaURL(value)
	if err != nil {
		t.Fatalf("normalizeMediaURL returned error: %v", err)
	}
	if normalized != value {
		t.Fatalf("expected %q, got %q", value, normalized)
	}
}

func TestNormalizeMediaURL_AllowsGitHubUserAttachmentRedirectHost(t *testing.T) {
	value := "https://github-production-user-asset-6210df.s3.amazonaws.com/34227344/578594059-demo.png"

	normalized, err := normalizeMediaURL(value)
	if err != nil {
		t.Fatalf("normalizeMediaURL returned error: %v", err)
	}
	if normalized != value {
		t.Fatalf("expected %q, got %q", value, normalized)
	}
}

func TestProxyServiceFetchAndCache(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	first, err := service.FetchAndCache(context.Background(), upstream.URL+"/asset.png")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if first.Metadata.ContentType != "image/png" {
		t.Fatalf("unexpected content type: %q", first.Metadata.ContentType)
	}

	second, ok, err := service.GetCached(upstream.URL + "/asset.png")
	if err != nil {
		t.Fatalf("second get cached: %v", err)
	}
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if second.Metadata.Size != first.Metadata.Size {
		t.Fatalf("expected cached sizes to match, got %d vs %d", second.Metadata.Size, first.Metadata.Size)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected one upstream hit, got %d", got)
	}
}

func TestProxyServiceProxyPass(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-3" {
			t.Fatalf("expected range header to be forwarded, got %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}

	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	resp, err := service.ProxyPass(context.Background(), upstream.URL+"/demo.mp4", "bytes=0-3", false)
	if err != nil {
		t.Fatalf("proxy pass: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read proxied body: %v", err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes 0-3/10" {
		t.Fatalf("unexpected content-range: %q", resp.Header.Get("Content-Range"))
	}
	if string(body) != "data" {
		t.Fatalf("unexpected proxied body %q", string(body))
	}
}

// INT-171 内容类型准入矩阵：用户态媒体面拒 svg（含非标准写法）与 HTML 等
// 非图片/视频类型，收位图 + 视频；截图面（imagesOnly）位图 only 不回归。
// 空 Content-Type 与 octet-stream 走扩展名回退，集合里没有 .svg。
func TestIsAllowedContentTypeGateMatrix(t *testing.T) {
	mediaFace := newService(t.TempDir(), serviceConfig{})
	screenshotFace := newService(t.TempDir(), serviceConfig{
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
		imagesOnly:   true,
	})

	cases := []struct {
		name        string
		contentType string
		rawURL      string
		mediaWant   bool
		screenWant  bool
	}{
		{"svg standard", "image/svg+xml", "https://x.test/a.svg", false, false},
		{"svg non-standard", "image/svg", "https://x.test/a.svg", false, false},
		{"svg case-insensitive", "IMAGE/SVG+XML", "https://x.test/a", false, false},
		{"html", "text/html", "https://x.test/a.html", false, false},
		{"xhtml", "application/xhtml+xml", "https://x.test/a.xhtml", false, false},
		{"plain text", "text/plain", "https://x.test/a.txt", false, false},
		{"png", "image/png", "https://x.test/a.png", true, true},
		{"jpeg", "image/jpeg", "https://x.test/a.jpg", true, true},
		{"gif", "image/gif", "https://x.test/a.gif", true, true},
		{"webp", "image/webp", "https://x.test/a.webp", true, true},
		{"mp4", "video/mp4", "https://x.test/a.mp4", true, false},
		{"empty content type svg ext", "", "https://x.test/a.svg", false, false},
		{"empty content type png ext", "", "https://x.test/a.png", true, true},
		{"empty content type mp4 ext", "", "https://x.test/a.mp4", true, false},
		{"octet stream svg ext", "application/octet-stream", "https://x.test/a.svg", false, false},
		{"octet stream png ext", "application/octet-stream", "https://x.test/a.png", true, true},
		{"octet stream mp4 ext", "application/octet-stream", "https://x.test/a.mp4", true, false},
		{"png with parameter", "image/png; charset=binary", "https://x.test/a.png", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/media-face", func(t *testing.T) {
			if got := mediaFace.isAllowedContentType(tc.contentType, tc.rawURL); got != tc.mediaWant {
				t.Fatalf("media face isAllowedContentType(%q, %q) = %v, want %v", tc.contentType, tc.rawURL, got, tc.mediaWant)
			}
		})
		t.Run(tc.name+"/screenshot-face", func(t *testing.T) {
			if got := screenshotFace.isAllowedContentType(tc.contentType, tc.rawURL); got != tc.screenWant {
				t.Fatalf("screenshot face isAllowedContentType(%q, %q) = %v, want %v", tc.contentType, tc.rawURL, got, tc.screenWant)
			}
		})
	}
}

// INT-171 ProxyPass 同闸门：svg / html / 空 Content-Type + .svg 扩展名的
// 直通请求一律 ErrUnsupportedUpstreamMedia，整文件下载、Range、HEAD 三种
// 形态都不放行；位图/视频的 Range 与 HEAD 透传不受影响。
func TestProxyPassEnforcesContentTypeGate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Content-Type 由 ct 查询参数控制；empty 参数时不写 body——不写就
		// 不会触发 Go 的嗅探补头，才能模拟「上游没标 Content-Type」。
		if ct := r.URL.Query().Get("ct"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/8")
			w.WriteHeader(http.StatusPartialContent)
		}
		if r.URL.Query().Has("empty") {
			return
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	rejected := []struct {
		name        string
		path        string
		headOnly    bool
		rangeHeader string
	}{
		{"svg content type", "/asset.svg?ct=image/svg%2bxml", false, ""},
		{"svg content type head", "/asset.svg?ct=image/svg%2bxml", true, ""},
		{"svg content type range", "/asset.svg?ct=image/svg%2bxml", false, "bytes=0-3"},
		{"html content type", "/asset.html?ct=text/html", false, ""},
		{"svg extension no content type", "/asset.svg?empty", false, ""},
		{"svg extension head no content type", "/asset.svg?empty", true, ""},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := service.ProxyPass(context.Background(), upstream.URL+tc.path, tc.rangeHeader, tc.headOnly)
			if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
				t.Fatalf("err = %v, want ErrUnsupportedUpstreamMedia", err)
			}
			if resp != nil {
				t.Fatalf("resp = %+v, want nil", resp)
			}
		})
	}

	allowed := []struct {
		name        string
		path        string
		headOnly    bool
		rangeHeader string
	}{
		{"mp4 range passthrough", "/clip.mp4?ct=video/mp4", false, "bytes=0-3"},
		{"mp4 head extension fallback", "/clip.mp4?empty", true, ""},
		{"png full download", "/pic.png?ct=image/png", false, ""},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := service.ProxyPass(context.Background(), upstream.URL+tc.path, tc.rangeHeader, tc.headOnly)
			if err != nil {
				t.Fatalf("proxy pass: %v", err)
			}
			defer resp.Body.Close()
			if tc.rangeHeader != "" && resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("status = %d, want 206", resp.StatusCode)
			}
		})
	}
}

// 扩展名回退放行时类型改写：上游 Content-Type 为空（写 nil 抑制 Go 自动
// 嗅探补头）或 application/octet-stream 时，按扩展名准入的资源回给客户端/
// 写进缓存的是表内具体媒体类型——回包统一带 nosniff，octet-stream/空类型
// 不会被浏览器嗅探成图片，<img>/<video> 会拒收。
func TestContentTypeFallbackNormalizesToConcreteMediaType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("ct") {
		case "none":
			// 置 nil 才不被 Go 在写 body 时自动嗅探补上 Content-Type。
			w.Header()["Content-Type"] = nil
		default:
			w.Header().Set("Content-Type", r.URL.Query().Get("ct"))
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	// 空 Content-Type + .png：放行且回包类型是 image/png（不是空、不是
	// octet-stream）。
	resp, err := service.ProxyPass(context.Background(), upstream.URL+"/asset.png?ct=none", "", false)
	if err != nil {
		t.Fatalf("empty content type proxy pass: %v", err)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		resp.Body.Close()
		t.Fatalf("content type = %q, want image/png", got)
	}
	resp.Body.Close()

	// 空 Content-Type + .svg：扩展名回退集合没有 .svg，拒。
	resp, err = service.ProxyPass(context.Background(), upstream.URL+"/asset.svg?ct=none", "", false)
	if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
	if resp != nil {
		t.Fatalf("resp = %+v, want nil", resp)
	}

	// octet-stream + .mp4：ProxyPass 放行且回包 video/mp4。
	resp, err = service.ProxyPass(context.Background(), upstream.URL+"/clip.mp4?ct=application/octet-stream", "", false)
	if err != nil {
		t.Fatalf("octet-stream proxy pass: %v", err)
	}
	if got := resp.Header.Get("Content-Type"); got != "video/mp4" {
		resp.Body.Close()
		t.Fatalf("content type = %q, want video/mp4", got)
	}
	resp.Body.Close()

	// octet-stream + .mp4：FetchAndCache 写进缓存 meta 的也是 video/mp4。
	svc2 := newProxyService(t.TempDir(), time.Hour, 1<<20, upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host })
	cached, err := svc2.FetchAndCache(context.Background(), upstream.URL+"/clip.mp4?ct=application/octet-stream")
	if err != nil {
		t.Fatalf("octet-stream fetch: %v", err)
	}
	if cached.Metadata.ContentType != "video/mp4" {
		t.Fatalf("cached content type = %q, want video/mp4", cached.Metadata.ContentType)
	}
	metaBytes, err := os.ReadFile(svc2.metaPath(cached.Metadata.URL, ""))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if entry.ContentType != "video/mp4" {
		t.Fatalf("meta content type = %q, want video/mp4", entry.ContentType)
	}
}

// Content-Type 歧义即拒：header 里多个 Content-Type 值、或单值逗号拼接，
// 整文件与 Range 两条路径都按 ErrUnsupportedUpstreamMedia 拒，不放行也
// 不取任一值规范化。
func TestAmbiguousContentTypeRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi.png":
			w.Header().Add("Content-Type", "image/png")
			w.Header().Add("Content-Type", "image/svg+xml")
		case "/joined.png":
			w.Header().Set("Content-Type", "image/png, image/svg+xml")
		}
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/8")
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	for _, tc := range []struct {
		name        string
		path        string
		rangeHeader string
	}{
		{"multi value full", "/multi.png", ""},
		{"multi value range", "/multi.png", "bytes=0-3"},
		{"comma joined full", "/joined.png", ""},
		{"comma joined range", "/joined.png", "bytes=0-3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := service.ProxyPass(context.Background(), upstream.URL+tc.path, tc.rangeHeader, false)
			if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
				t.Fatalf("err = %v, want ErrUnsupportedUpstreamMedia", err)
			}
			if resp != nil {
				t.Fatalf("resp = %+v, want nil", resp)
			}
			if _, err := service.FetchAndCache(context.Background(), upstream.URL+tc.path); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
				t.Fatalf("fetch err = %v, want ErrUnsupportedUpstreamMedia", err)
			}
		})
	}
}

// Content-Type 参数里的逗号不是歧义：`video/mp4; codecs="avc1.42E01E,
// mp4a.40.2"` 这类合法写法照常放行，回包是去掉参数的基础类型；只有基础
// 类型本身含逗号（多值拼接）才算歧义（见 TestAmbiguousContentTypeRejected）。
func TestContentTypeParametersAccepted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.URL.Query().Get("ct"))
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	for _, tc := range []struct{ name, ct, want string }{
		{"codecs 参数带逗号", `video/mp4; codecs="avc1.42E01E, mp4a.40.2"`, "video/mp4"},
		{"普通参数带逗号", "image/png; foo=bar,baz", "image/png"},
		{"大写与参数", "IMAGE/PNG; charset=binary", "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := service.ProxyPass(context.Background(),
				upstream.URL+"/asset.png?ct="+url.QueryEscape(tc.ct), "", false)
			if err != nil {
				t.Fatalf("proxy pass: %v", err)
			}
			defer resp.Body.Close()
			if got := resp.Header.Get("Content-Type"); got != tc.want {
				t.Fatalf("content type = %q, want %q（参数被去掉）", got, tc.want)
			}
		})
	}
}

// 扩展名回退用重定向之后的最终 URL：a.png 302 到 b.svg 且最终类型是
// octet-stream 时必须拒（借原始扩展名放行会把 svg 字节按 png 回客户端）；
// 重定向到 c.png 的 octet-stream 则按最终扩展名放行并规范化成 image/png。
// 用 upstream.Client() 让注入客户端真实跟随跳转。
func TestExtensionFallbackUsesFinalRedirectURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.png":
			http.Redirect(w, r, "/b.svg?final=1", http.StatusFound)
		case "/to-png.png":
			http.Redirect(w, r, "/c.png?final=1", http.StatusFound)
		case "/b.svg", "/c.png":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("data"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(
		t.TempDir(),
		time.Hour,
		1<<20,
		upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host },
	)

	if _, err := service.FetchAndCache(context.Background(), upstream.URL+"/a.png"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("fetch err = %v, want ErrUnsupportedUpstreamMedia（最终 .svg 不得借 .png 放行）", err)
	}
	resp, err := service.ProxyPass(context.Background(), upstream.URL+"/a.png", "bytes=0-3", false)
	if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("range err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
	if resp != nil {
		t.Fatalf("resp = %+v, want nil", resp)
	}

	cached, err := service.FetchAndCache(context.Background(), upstream.URL+"/to-png.png")
	if err != nil {
		t.Fatalf("fetch to-png: %v", err)
	}
	if cached.Metadata.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png（按最终 URL 扩展名规范化）", cached.Metadata.ContentType)
	}
}

// WithHTTPTransport 撞上 guardAddress（截图面）直接 panic：那个面的 SSRF
// 防线是 Transport 里的拨号期 IP 校验，整颗换 Transport 会把它静默拆掉。
// 选项是导出的，宁可炸也不要让接线错误变成静默失守。
func TestWithHTTPTransportRejectsGuardedFace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when injecting transport on guardAddress face")
		}
	}()
	newService(t.TempDir(), serviceConfig{
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
		imagesOnly:   true,
		guardAddress: true,
		transport:    http.DefaultTransport,
	})
}

// svg 拒收带独立哨兵（包装通用哨兵）：出口据此只给 svg 加可缓存的 415，
// 其它非媒体类型（text/html 之类可能是上游拦截页）不钉缓存。
func TestSVGRejectionCarriesDedicatedSentinel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		case "/b.svg":
			// 上游没标类型：靠 .svg 扩展名判 svg。
			w.Header()["Content-Type"] = nil
		case "/c.png":
			w.Header().Set("Content-Type", "text/html")
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	service := newProxyService(t.TempDir(), time.Hour, 1<<20, upstream.Client(),
		func(u *url.URL) bool { return u.Host == parsed.Host })

	for _, path := range []string{"/a.svg", "/b.svg"} {
		_, err := service.FetchAndCache(context.Background(), upstream.URL+path)
		if !errors.Is(err, ErrUnsupportedSVG) {
			t.Fatalf("%s err = %v, want ErrUnsupportedSVG", path, err)
		}
		if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
			t.Fatalf("%s err = %v, want 同时命中 ErrUnsupportedUpstreamMedia", path, err)
		}
	}

	// text/html 不是 svg：只命中通用哨兵，出口不会给它加缓存头。
	_, err = service.FetchAndCache(context.Background(), upstream.URL+"/c.png")
	if errors.Is(err, ErrUnsupportedSVG) {
		t.Fatalf("text/html err = %v, must not be ErrUnsupportedSVG", err)
	}
	if !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("text/html err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
}

// 代理地址带版本参数：媒体响应的安全语义变了就递增，浏览器缓存键随之变化，
// 旧版本 URL 上缓存过的响应（例如放行时期的 svg）不会再被命中。
func TestBuildProxyURLCarriesVersion(t *testing.T) {
	got := BuildProxyURL("https://github.com/user-attachments/assets/demo.png")
	want := ProxyPath + "?url=" + url.QueryEscape("https://github.com/user-attachments/assets/demo.png") + "&v=" + ProxyURLVersion
	if got != want {
		t.Fatalf("BuildProxyURL = %q, want %q", got, want)
	}
}

// 下载文件名兜底用：具体媒体类型反查扩展名，表外类型 .bin。
func TestMediaFileExtension(t *testing.T) {
	for _, tc := range []struct{ contentType, want string }{
		{"image/png", ".png"},
		{"image/jpeg", ".jpeg"},
		{"video/mp4", ".mp4"},
		{"IMAGE/WEBP", ".webp"},
		{"text/html", ".bin"},
		{"", ".bin"},
	} {
		if got := MediaFileExtension(tc.contentType); got != tc.want {
			t.Fatalf("MediaFileExtension(%q) = %q, want %q", tc.contentType, got, tc.want)
		}
	}
}

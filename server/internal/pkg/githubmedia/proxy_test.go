package githubmedia

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

package githubmedia

import (
	"context"
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

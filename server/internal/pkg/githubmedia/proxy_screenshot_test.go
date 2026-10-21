package githubmedia

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// 截图下载面（NewReviewMediaService）的行为：任意公网 https 图片、只收位图、
// 拨号时校验解析 IP 拒绝内网/保留地址；与 GitHub 白名单面互不影响。

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},          // 回环
		{"10.0.0.5", true},           // RFC1918
		{"172.16.0.1", true},         // RFC1918
		{"192.168.1.1", true},        // RFC1918
		{"169.254.1.1", true},        // 链路本地
		{"100.64.1.1", true},         // CGNAT
		{"198.18.0.1", true},         // 基准测试保留段
		{"240.0.0.1", true},          // 240.0.0.0/4 保留段
		{"0.0.0.0", true},            // 未指定（0.0.0.0/8 全段同拒）
		{"255.255.255.255", true},    // 广播
		{"::1", true},                // IPv6 回环
		{"fd00::1", true},            // IPv6 ULA
		{"fe80::1", true},            // IPv6 链路本地
		{"64:ff9b::a9fe:a9fe", true}, // NAT64 /96 内嵌 169.254.169.254
		// RFC 6052 /48 布局：前缀在 0-5、IPv4 高 16 位在 6-7、u 八位组在 8、
		// 低 16 位在 9-10——64:ff9b:1:a00:0:100:: 内嵌的正是 10.0.0.1。
		// local-use /48 整体拒绝，内嵌公网地址的同前缀地址（下一行）也拒：
		// 布局可变、按固定偏移解码不可靠，宁可错拒不可放过。
		{"64:ff9b:1:a00:0:100::", true},
		{"64:ff9b:1:808:808:0::", true},
		// 旧用例曾把 64:ff9b:1::808:808 标成「/48 内嵌 8.8.8.8，放行」：那是
		// 拿 /96 的末 4 字节布局误读 /48。/48 不再解码、整体拒绝。
		{"64:ff9b:1::808:808", true},
		// /32 内非 /96 的非 local-use 前缀同样整体拒绝：64:ff9b:a00:0:100::
		// 按 /48 式布局内嵌 10.0.0.1，旧实现既解不出内嵌 IPv4 也不整体拒，
		// 当公网放行；统一规则下 bytes 4-11 非全零 → 整体拒绝。
		{"64:ff9b:a00:0:100::", true},
		{"2002:c0a8:0101::", true}, // 6to4 内嵌 192.168.1.1
		// Teredo（RFC 4380 真实布局）：0-3 前缀、4-7 服务器 IPv4、8-9 flags、
		// 10-11 混淆端口、12-15 客户端 IPv4 按位取反存储。
		{"2001:0:0102:0304:4000:abcd:f5ff:fffe", true},  // Teredo 内嵌 10.0.0.1（f5ff:fffe 取反）
		{"2001:0:1111:2222:4000:abcd:3f57:fefe", true},  // Teredo 内嵌 192.168.1.1（3f57:fefe 取反）
		{"::a9fe:a9fe", true},                           // IPv4-compatible 内嵌 169.254.169.254
		{"64:ff9b::808:808", false},                     // NAT64 /96 内嵌 8.8.8.8，公网放行
		{"2002:0808:0808::", false},                     // 6to4 内嵌 8.8.8.8，公网放行
		{"2001:0:0102:0304:4000:abcd:f7f7:f7f7", false}, // Teredo 内嵌 8.8.8.8，放行证明不是整个 2001::/32 一律拒
		{"::808:808", false},                            // IPv4-compatible 内嵌 8.8.8.8，公网放行
		{"1.1.1.1", false},                              // 公网
		{"8.8.8.8", false},                              // 公网
		{"2606:4700:4700::1111", false},                 // 公网 IPv6
	}
	for _, c := range cases {
		if got := isBlockedIP(parseTestIP(t, c.ip)); got != c.blocked {
			t.Fatalf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
	if !isBlockedIP(nil) {
		t.Fatal("nil IP 应视为阻塞")
	}
}

func parseTestIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("parse ip %q", s)
	}
	return ip
}

// guardAddress: true 的服务对回环上游（httptest 即 127.0.0.1）拨号即拒。
func TestScreenshotServiceBlocksLoopbackUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	defer upstream.Close()

	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:  "review-screenshot-cache",
		allowURL:     allowPublicImageHost,
		guardAddress: true,
	})
	_, err := svc.FetchAndCache(context.Background(), upstream.URL+"/shot.png")
	if !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress", err)
	}
}

// 非 GitHub 域名的公网图片（测试里是任意 httptest 主机）应能下载落盘；
// 内容类型与大小上限语义按 imagesOnly 收紧。
func TestScreenshotServiceDownloadsPublicImages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/shot.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-data"))
		case "/chunked.png":
			// 先写头再 flush：强制分块传输，响应无 Content-Length（旧的
			// contentLength<=0 判定会把它误判成 413）。
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			_, _ = w.Write([]byte("chunked-png-data"))
			flusher.Flush()
		case "/blob.png":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("octet-png-data"))
		case "/clip.mp4":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("not-a-video"))
		default:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		}
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})

	// image/png 直接成功落盘。
	cached, err := svc.FetchAndCache(context.Background(), upstream.URL+"/shot.png")
	if err != nil {
		t.Fatalf("png fetch: %v", err)
	}
	if cached.Metadata.ContentType != "image/png" || cached.Metadata.Size != int64(len("png-data")) {
		t.Fatalf("cached = %+v", cached.Metadata)
	}

	// 无 Content-Length 的分块图片也应成功（ContentLength 误判修复）。
	chunked, err := svc.FetchAndCache(context.Background(), upstream.URL+"/chunked.png")
	if err != nil {
		t.Fatalf("chunked png fetch: %v", err)
	}
	if chunked.Metadata.Size != int64(len("chunked-png-data")) {
		t.Fatalf("chunked size = %d, want %d", chunked.Metadata.Size, len("chunked-png-data"))
	}

	// octet-stream + 图片扩展名 → 收；+ 视频扩展名 → 拒（imagesOnly）。
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/blob.png"); err != nil {
		t.Fatalf("octet-stream png: %v", err)
	}
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/clip.mp4"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("mp4 err = %v, want ErrUnsupportedUpstreamMedia", err)
	}

	// text/plain 被拒。
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/notes.txt"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("text err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
}

// F27 回归：上游没有 Content-Type 时按位图扩展名回退——位图扩展名收，
// 非位图扩展名拒（ErrUnsupportedUpstreamMedia）。响应头里真没有 Content-Type
// 的写法是把头置 nil：Go 服务器只在头不存在时做内容嗅探补头，置 nil 既压掉
// 嗅探又不会写出到响应。
func TestScreenshotServiceMissingContentTypeFallsBackToExtension(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		switch r.URL.Path {
		case "/shot.png":
			_, _ = w.Write([]byte("png-data"))
		default:
			_, _ = w.Write([]byte("hello"))
		}
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	// 先证明上游响应确实没有 Content-Type：置 nil 的技巧失效时 Go 会嗅探补头
	// （PNG 魔数会被嗅成 image/png），下面的断言就成了假绿。
	resp, err := upstream.Client().Get(upstream.URL + "/shot.png")
	if err != nil {
		t.Fatalf("probe upstream: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "" {
		t.Fatalf("上游 Content-Type = %q, want 空（测试前置不成立）", got)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})

	// 无 Content-Type + 位图扩展名 → 收。
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/shot.png"); err != nil {
		t.Fatalf("missing content-type + .png: %v", err)
	}
	// 无 Content-Type + 非位图扩展名 → 拒。
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/notes.txt"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("missing content-type + .txt err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
}

// 重定向复检必须用实例自身的 allowURL（旧实现写死 GitHub 白名单）：只放行
// A 主机的服务遇到 A→B 的 302 应在重定向处拒绝。
func TestScreenshotRedirectCheckedAgainstInstanceAllowList(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/final.png", http.StatusFound)
	}))
	defer origin.Close()

	parsedOrigin, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("parse origin url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsedOrigin.Host },
	})
	_, err = svc.FetchAndCache(context.Background(), origin.URL+"/start.png")
	if !errors.Is(err, ErrInvalidMediaURL) {
		t.Fatalf("err = %v, want ErrInvalidMediaURL（重定向目标不在实例白名单）", err)
	}
}

// 正向：实例白名单同时放行 A、B 时，A→B 的 302 应照常下载。只测拒绝方向的
// 用例在旧 bug（CheckRedirect 写死 GitHub 白名单）下也会假绿，这条补上放行
// 方向。
func TestScreenshotRedirectFollowedWithinInstanceAllowList(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	defer final.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/final.png", http.StatusFound)
	}))
	defer origin.Close()

	parsedOrigin, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("parse origin url: %v", err)
	}
	parsedFinal, err := url.Parse(final.URL)
	if err != nil {
		t.Fatalf("parse final url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsedOrigin.Host || u.Host == parsedFinal.Host },
	})
	cached, err := svc.FetchAndCache(context.Background(), origin.URL+"/start.png")
	if err != nil {
		t.Fatalf("redirect download: %v", err)
	}
	if cached.Metadata.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", cached.Metadata.ContentType)
	}
	data, err := os.ReadFile(cached.Path)
	if err != nil {
		t.Fatalf("read cached: %v", err)
	}
	if string(data) != "png-data" {
		t.Fatalf("cached content = %q, want png-data", data)
	}
}

// F21 回归：同 URL 已有下载持锁时，等待必须吃 ctx——30s 探测时限是总时限，
// 不能被同 URL 前一请求的 45s 客户端超时拖穿（旧实现是 sync.Mutex，会干等
// 到对方超时为止）。
func TestFetchAndCacheDownloadWaitRespectsContext(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    allowPublicImageHost,
	})
	normalized, err := svc.normalize("http://example.com/held.png")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// 占住闸门：模拟同 URL 已有下载在跑。
	lock := svc.acquireDownloadLock(normalized)
	lock <- struct{}{}
	defer func() { <-lock }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := svc.FetchAndCache(ctx, normalized); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("等待 %v, 应在 ctx 时限（80ms）放弃而不是等满客户端超时", elapsed)
	}
}

func TestScreenshotServiceRequiresHTTPS(t *testing.T) {
	svc := NewReviewMediaService(t.TempDir())
	if _, err := svc.FetchAndCache(context.Background(), "http://uiv.example.com/f/x.png"); !errors.Is(err, ErrInvalidMediaURL) {
		t.Fatalf("err = %v, want ErrInvalidMediaURL", err)
	}
	if _, err := svc.normalize("https://uiv.example.com/f/x.png"); err != nil {
		t.Fatalf("https normalize: %v", err)
	}
	if !strings.Contains(svc.cacheDir, "review-screenshot-cache") {
		t.Fatalf("cacheDir = %q, want review-screenshot-cache 子目录", svc.cacheDir)
	}
}

// guardAddress 的客户端必须自己拨目标（不走环境代理）才能校验目标 IP；
// GitHub 媒体面不 guard，保留 ProxyFromEnvironment。
func TestGuardedClientProxyPolicy(t *testing.T) {
	guarded := newGuardedClient(true, allowPublicImageHost, true)
	gtr, ok := guarded.Transport.(*http.Transport)
	if !ok || gtr.Proxy != nil {
		t.Fatal("guard 客户端 transport proxy 应为 nil（拨号校验要求直连目标）")
	}
	plain := newGuardedClient(false, isAllowedGitHubMediaURL, false)
	ptr, ok := plain.Transport.(*http.Transport)
	if !ok || ptr.Proxy == nil {
		t.Fatalf("GitHub 媒体面 transport proxy 不应被清空，want ProxyFromEnvironment")
	}
}

// Bearer 令牌只准发给 GitHub 媒体域：外部图床的上游请求头里不能出现
// Authorization（攻击者在 PR 描述放外链即可换走 App 安装令牌）。
func TestFetchWithBearerNeverSendsTokenToExternalHost(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})
	if _, err := svc.FetchAndCacheWithBearer(context.Background(), upstream.URL+"/x.png", "secret-token"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("外部图床收到了 Authorization: %q, want 不发送", gotAuth)
	}
}

func TestIsGitHubMediaURL(t *testing.T) {
	yes := []string{
		"https://github.com/user-attachments/assets/abc",
		"https://private-user-images.githubusercontent.com/abc",
		"https://github-production-user-asset-6210df1.s3.amazonaws.com/x",
	}
	no := []string{
		"https://uiv.example.com/f/x.png",
		"https://evil.example.com/github.com/user-attachments/assets/abc",
		"github.com/user-attachments/assets/abc", // 无 scheme：解析后 Host 为空
		"", ":", "not a url",
	}
	for _, raw := range yes {
		if !IsGitHubMediaURL(raw) {
			t.Fatalf("IsGitHubMediaURL(%q) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if IsGitHubMediaURL(raw) {
			t.Fatalf("IsGitHubMediaURL(%q) = true, want false", raw)
		}
	}
}

// 200 + 空响应体（已知 Content-Length: 0 与分块空 body 两种）都不是可用截图。
func TestEmptyUpstreamMediaRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/empty.png":
			// 不写 body：Go 会补 Content-Length: 0。
		case "/chunked-empty.png":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush() // 强制分块：Content-Length 未知（-1）
		}
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})
	for _, p := range []string{"/empty.png", "/chunked-empty.png"} {
		if _, err := svc.FetchAndCache(context.Background(), upstream.URL+p); !errors.Is(err, ErrEmptyUpstreamMedia) {
			t.Fatalf("%s err = %v, want ErrEmptyUpstreamMedia", p, err)
		}
	}
}

// 本地超限（已知长度与流式写超两种路径）都报 ErrMediaTooLarge，文案固定中文。
func TestMediaTooLargeSentinel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/known.png":
			// Go 自动补 Content-Length，走「已知长度超限」判定。
			_, _ = w.Write(make([]byte, 64))
		case "/chunked.png":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = w.Write(make([]byte, 64)) // 分块：走 io.Copy 后复核判定
		}
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:       "review-screenshot-cache",
		allowURL:          func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:        true,
		maxCacheEntrySize: 8,
		client:            upstream.Client(),
	})
	for _, p := range []string{"/known.png", "/chunked.png"} {
		_, err := svc.FetchAndCache(context.Background(), upstream.URL+p)
		if !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("%s err = %v, want ErrMediaTooLarge", p, err)
		}
		if err.Error() != "图片超过大小上限" {
			t.Fatalf("err text = %q, want 图片超过大小上限", err.Error())
		}
	}
}

// 截图面只吃位图：svg 不按扩展名收，image/svg+xml 内容类型也拒。
func TestScreenshotFaceRejectsSVG(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte("<svg/>"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})
	if _, err := svc.FetchAndCache(context.Background(), upstream.URL+"/icon.svg"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("svg err = %v, want ErrUnsupportedUpstreamMedia", err)
	}
}

// GitHub 媒体代理面行为不变：仍只放行 GitHub 域名，允许 http/https。
func TestProxyServiceKeepsGitHubAllowList(t *testing.T) {
	svc := NewProxyService(t.TempDir())
	if _, err := svc.normalize("https://github.com/user-attachments/assets/xyz"); err != nil {
		t.Fatalf("github normalize: %v", err)
	}
	if _, err := svc.normalize("https://uiv.example.com/f/x.png"); !errors.Is(err, ErrInvalidMediaURL) {
		t.Fatalf("err = %v, want ErrInvalidMediaURL（GitHub 白名单不收外部图床）", err)
	}
	if svc.requireHTTPS || svc.imagesOnly {
		t.Fatal("GitHub 媒体面语义应保持不变（http/https、图片视频、无拨号校验）")
	}
	tr, ok := svc.client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("GitHub 媒体面应保留环境代理（不 guard）")
	}
}

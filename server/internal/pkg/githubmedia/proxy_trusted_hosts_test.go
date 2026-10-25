package githubmedia

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// INT-168 可信内网主机名单（review_media.trusted_private_hosts）：名单内
// 主机名跳过拨号 IP 检查——内网 DNS 把自建服务（UIV 等）直接解析到内网
// 地址的部署靠它放行；名单外主机、以及名单内主机 302 跳出的建连照旧走
// guardDialControl。匹配按 DialContext 收到的拨号主机名完全一致（不区分
// 大小写、不含端口、不支持通配符），不看解析出的 IP——同一内网地址上
// 挂着的其他服务不会被连带放行。

// trustedHostTestConfig 截图下载面语义（guardAddress + imagesOnly + 任意
// host 准入），requireHTTPS 关掉让 httptest 上场。
func trustedHostTestConfig(trusted []string) serviceConfig {
	return serviceConfig{
		cacheSubdir:         "review-screenshot-cache",
		allowURL:            allowPublicImageHost,
		imagesOnly:          true,
		guardAddress:        true,
		trustedPrivateHosts: trusted,
	}
}

// pngUpstream 位图上游：任意路径返回 image/png。
func pngUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// localhostURL 把 httptest 的 127.0.0.1 地址换成 localhost：拨号主机名走
// 「主机名解析出回环地址」的路径，而不是 IP 字面量直连。
func localhostURL(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	return fmt.Sprintf("http://%s%s", localhostHostPort(t, srv), path)
}

// localhostHostPort 上游的 localhost:port 键：动态名单项按 host:port
// 匹配，测试名单要写这个形态才会命中。
func localhostHostPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split upstream host: %v", err)
	}
	return "localhost:" + port
}

// 名单内主机解析到内网地址可以下载：localhost 解析到回环，名单写大写的
// LOCALHOST（顺带验证匹配不区分大小写）即放行。
func TestReviewMediaTrustedPrivateHostSkipsInternalCheck(t *testing.T) {
	upstream := pngUpstream(t)
	svc := newService(t.TempDir(), trustedHostTestConfig([]string{"LOCALHOST"}))
	cached, err := svc.FetchAndCache(context.Background(), localhostURL(t, upstream, "/ok.png"))
	if err != nil {
		t.Fatalf("trusted host fetch: %v", err)
	}
	if cached.Metadata.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", cached.Metadata.ContentType)
	}
}

// 匹配按拨号主机名，不看解析出的 IP：名单只有 localhost 时，同一台服务器
// 用 127.0.0.1 字面量直连仍被拒——放行的是名字不是地址。
func TestReviewMediaTrustedListMatchesHostnameNotResolvedIP(t *testing.T) {
	upstream := pngUpstream(t)
	svc := newService(t.TempDir(), trustedHostTestConfig([]string{"localhost"}))
	_, err := svc.FetchAndCache(context.Background(), upstream.URL+"/ok.png")
	if !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress", err)
	}
}

// 空名单与旧行为一致：localhost 解析回环照旧被拒。
func TestReviewMediaEmptyTrustedListStillGuards(t *testing.T) {
	upstream := pngUpstream(t)
	svc := newService(t.TempDir(), trustedHostTestConfig(nil))
	_, err := svc.FetchAndCache(context.Background(), localhostURL(t, upstream, "/ok.png"))
	if !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress", err)
	}
}

// 名单内主机 302 跳到名单外内网主机仍被拒：每次新建连都按拨号主机名
// 重新判定。
func TestReviewMediaRedirectFromTrustedToUntrustedBlocked(t *testing.T) {
	target := pngUpstream(t)
	var targetURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	targetURL = target.URL + "/ok.png"

	svc := newService(t.TempDir(), trustedHostTestConfig([]string{"localhost"}))
	_, err := svc.FetchAndCache(context.Background(), localhostURL(t, origin, "/start"))
	if !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress（名单内主机跳名单外内网主机仍应被拒）", err)
	}
}

// 动态名单（allow_private_network 集成主机随拨号现查）走私网口径守卫，
// 不是裸放行——与 Forgejo API 面 AllowPrivateNetwork 同策略：RFC1918/
// ULA/CGNAT 放行，回环/链路本地/元数据恒拒。localhost 解析回环，即使
// 主机名在动态名单里也必须被拦（裸拨号路径下本用例会成功）。
func TestReviewMediaDynamicTrustedHostFunc(t *testing.T) {
	upstream := pngUpstream(t)
	cfg := trustedHostTestConfig(nil)
	trusted := []string{localhostHostPort(t, upstream)}
	cfg.trustedPrivateHostsFunc = func() []string { return trusted }
	svc := newService(t.TempDir(), cfg)
	if _, err := svc.FetchAndCache(context.Background(), localhostURL(t, upstream, "/ok.png")); !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress（动态名单主机解析回环应被私网口径恒拒）", err)
	}
	// 名单撤销后同一主机仍被拦——私网口径与公网口径对回环结论一致，
	// 此项防的是「名单失效后回落到无守卫路径」的回归。
	trusted = nil
	svc2 := newService(t.TempDir(), cfg)
	if _, err := svc2.FetchAndCache(context.Background(), localhostURL(t, upstream, "/ok2.png")); !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress（动态名单撤销后应恢复拦截）", err)
	}
}

// guardPrivateDialControl 的段表与 Forgejo API 面同判定源：RFC1918 与
// CGNAT 放行，回环/链路本地/云元数据（含 CGNAT 段内的阿里云端点）恒拒。
func TestGuardPrivateDialControl(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"10.0.0.1:443", false},       // RFC1918 放行
		{"172.16.5.4:443", false},     // RFC1918 放行
		{"192.168.1.2:443", false},    // RFC1918 放行
		{"100.64.1.1:443", false},     // CGNAT 放行
		{"8.8.8.8:443", false},        // 公网放行
		{"127.0.0.1:443", true},       // 回环恒拒
		{"169.254.169.254:443", true}, // 链路本地/元数据恒拒
		{"100.100.100.200:443", true}, // CGNAT 段内阿里云元数据恒拒
		{"0.0.0.0:443", true},         // 未指定恒拒
		{"224.0.0.1:443", true},       // 多播恒拒
	}
	for _, c := range cases {
		err := guardPrivateDialControl("", c.addr, nil)
		if c.blocked != errors.Is(err, ErrBlockedUpstreamAddress) {
			t.Fatalf("%s: err=%v, want blocked=%v", c.addr, err, c.blocked)
		}
	}
}

// 动态名单内主机跳到名单外内网主机同样被拦：每一跳独立判定。
func TestReviewMediaDynamicTrustedRedirectStillGuarded(t *testing.T) {
	target := pngUpstream(t)
	var targetURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	targetURL = target.URL + "/ok.png"

	cfg := trustedHostTestConfig(nil)
	cfg.trustedPrivateHostsFunc = func() []string { return []string{localhostHostPort(t, origin)} }
	svc := newService(t.TempDir(), cfg)
	_, err := svc.FetchAndCache(context.Background(), localhostURL(t, origin, "/start"))
	if !errors.Is(err, ErrBlockedUpstreamAddress) {
		t.Fatalf("err = %v, want ErrBlockedUpstreamAddress（动态名单内主机跳名单外仍应被拒）", err)
	}
}

// 动态名单按 host:port 匹配：放行只到配置过的端口，同主机其他端口不
// 连带信任（Codex R19：私网口径守卫放行 RFC1918，不限端口就会够到
// 同主机的其他服务）。大小写不敏感；静态名单仍是 host-only 不受影响。
func TestDynamicTrustedAddrMatch(t *testing.T) {
	hosts := []string{"forge.example.com:443", " [::1]:8443 "}
	for _, c := range []struct {
		addr string
		want bool
	}{
		{"forge.example.com:443", true},
		{"Forge.Example.COM:443", true},
		{"forge.example.com:8443", false}, // 同主机异端口不放行
		{"forge.example.com:80", false},
		{"other.example.com:443", false},
		{"[::1]:8443", true},
		{"::1:8443", false}, // 缺方括号 SplitHostPort 拆分失败
		{"no-port", false},
	} {
		if got := dynamicTrustedAddrMatch(c.addr, hosts); got != c.want {
			t.Fatalf("match(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// 名单规整：去空白、统一小写去重、丢弃空项。
func TestNormalizeTrustedPrivateHosts(t *testing.T) {
	set := normalizeTrustedPrivateHosts([]string{" UIV.Example.COM ", "uiv.example.com", "", "  ", "img.local"})
	if len(set) != 2 {
		t.Fatalf("set = %v, want 2 项（空白与重复规整后）", set)
	}
	for _, h := range []string{"uiv.example.com", "img.local"} {
		if _, ok := set[h]; !ok {
			t.Fatalf("set 缺 %q", h)
		}
	}
	if normalizeTrustedPrivateHosts(nil) != nil {
		t.Fatal("nil 输入应返回 nil")
	}
	if got := normalizeTrustedPrivateHosts([]string{" ", ""}); len(got) != 0 {
		t.Fatalf("全空项应规整为空集合，got %v", got)
	}
}

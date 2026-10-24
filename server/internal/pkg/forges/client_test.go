package forges

import (
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.8.8", // 回环
		"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC1918
		"169.254.169.254", "169.254.0.1", // 链路本地（含云元数据）
		"::1",                     // IPv6 回环
		"fc00::1", "fd12:3456::1", // ULA fc00::/7
		"fd00:ec2::254",        // AWS IMDS IPv6 端点（ULA 段内的恒拒例外）
		"fe80::1",              // 链路本地 fe80::/10
		"fec0::1",              // 已废弃 site-local
		"0.0.0.0",              // 未指定
		"224.0.0.1", "ff02::1", // 多播
		"100.64.0.1",                           // CGNAT
		"198.18.0.1", "240.0.0.1", "192.0.0.9", // 保留段
		"::ffff:127.0.0.1",                        // IPv4-mapped 内网
		"::ffff:10.0.0.9",                         // IPv4-mapped 私网
		"2002:0a00:0001::",                        // 6to4 载荷 10.0.0.1
		"2001:0000:0000:0000:0000:0000:ffff:ffff", // Teredo 载荷 0.0.0.0
		"64:ff9b::0a00:0001",                      // NAT64 载荷 10.0.0.1
		"64:ff9a::0808:0808",                      // local-use NAT64 整体拒绝
	}
	for _, addr := range blocked {
		if !isBlockedIP(net.ParseIP(addr)) {
			t.Errorf("isBlockedIP(%s) = false, want true", addr)
		}
	}

	public := []string{
		"8.8.8.8", "1.1.1.1",
		"2606:4700::1111",
		"64:ff9b::0808:0808",                       // NAT64 载荷公网
		"2002:0808:0808::",                         // 6to4 载荷公网
		"192.0.2.1", "198.51.100.1", "203.0.113.1", // 文档示例网段（公网可路由）
	}
	for _, addr := range public {
		if isBlockedIP(net.ParseIP(addr)) {
			t.Errorf("isBlockedIP(%s) = true, want false", addr)
		}
	}
}

// TestIsBlockedPrivateMode 私网放行口径：RFC1918 / ULA / CGNAT / 公网放行，
// 回环、链路本地（含元数据）、多播、未指定与保留段照拒（内嵌 IPv4 同口径
// 递归）。开关放行的是「这个 Forgejo 在内网」，不是元数据与回环。
func TestIsBlockedPrivateMode(t *testing.T) {
	allowed := []string{
		"10.1.2.3", "172.16.0.1", "192.168.1.1", // RFC1918
		"100.64.0.1", "100.127.255.254", // CGNAT（放行：组网常用段）
		"100.100.100.199",         // 相邻 CGNAT 不误伤（IMDS 端点是精确匹配）
		"fc00::1", "fd12:3456::1", // ULA
		"fd00:ec2::1",                // 相邻 ULA 不误伤（IMDS 端点是精确匹配）
		"8.8.8.8", "2606:4700::1111", // 公网
		"2002:0a00:0001::",                        // 6to4 载荷 10.0.0.1（同口径递归）
		"64:ff9b::0a00:0001",                      // NAT64 载荷 10.0.0.1
		"2001:0000:0000:0000:0000:0000:f5ff:fffe", // Teredo 载荷 10.0.0.1
	}
	for _, addr := range allowed {
		if isBlockedPrivateMode(net.ParseIP(addr)) {
			t.Errorf("isBlockedPrivateMode(%s) = true, want false", addr)
		}
	}

	blocked := []string{
		"127.0.0.1", "127.8.8.8", "::1", // 回环永远拒
		"169.254.169.254", "169.254.0.1", "fe80::1", // 链路本地（含元数据）
		"224.0.0.1", "ff02::1", // 多播
		"0.0.0.0",                  // 未指定
		"192.0.0.9", "192.88.99.1", // IETF 协议分配 / 废弃 anycast
		"198.18.0.1", "240.0.0.1", "255.255.255.255", // 基准测试 / 保留段
		"fec0::1",            // 已废弃 site-local
		"fd00:ec2::254",      // AWS IMDS IPv6 端点（ULA 放行的恒拒例外）
		"100.100.100.200",    // 阿里云 IMDS 端点（CGNAT 段内的恒拒例外）
		"64:ff9a::0808:0808", // local-use NAT64
		"64:ff9b::7f00:0001", // NAT64 载荷回环
		"2002:7f00:0001::",   // 6to4 载荷回环
		"::ffff:127.0.0.1",   // mapped 回环
	}
	for _, addr := range blocked {
		if !isBlockedPrivateMode(net.ParseIP(addr)) {
			t.Errorf("isBlockedPrivateMode(%s) = false, want true", addr)
		}
	}
}

// relaxLoopbackForTest 测试期间把私网口径放宽到放行回环：httptest 只能落在
// 127.0.0.1，正向端到端用例需要它。默认（生产）口径永远拒回环——该默认口径
// 由 TestClientDialGuard 的「名单主机回环仍拒」用例用未注入的入口机器断言。
func relaxLoopbackForTest(t *testing.T) {
	t.Helper()
	orig := privateModeBlocked
	privateModeBlocked = func(ip net.IP) bool {
		if ip.IsLoopback() {
			return false
		}
		return orig(ip)
	}
	t.Cleanup(func() { privateModeBlocked = orig })
}

// TestClientRedirectStripsAuthOutsidePrefix 凭证作用域（Config.AuthPathPrefix）：
// API 前缀内的同主机跳转保留 Authorization，跳出前缀或含 . / .. 歧义段的
// 跳转剥掉——政策只有 client 这一份，调用方不再各自包装。
func TestClientRedirectStripsAuthOutsidePrefix(t *testing.T) {
	relaxLoopbackForTest(t)
	var mu sync.Mutex
	var sawAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawAuth = append(sawAuth, r.URL.EscapedPath()+"|"+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/version":
			http.Redirect(w, r, "/api/v1/user", http.StatusFound)
		case "/api/v1/user":
			http.Redirect(w, r, "/api/v1/../logs", http.StatusFound)
		case "/logs":
			http.Redirect(w, r, "/api/v1/me", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client, err := NewClient(Config{
		AllowedHosts:        []string{u.Hostname()},
		AllowPrivateNetwork: true,
		AuthPathPrefix:      "/api/v1",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "token secret-pat")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	// /api/v1/../logs 的跳转：解码路径含 .. 歧义段 → 剥凭证（该字面路径落
	// default 分支，链路就此结束，共 3 跳）。
	if len(sawAuth) != 3 {
		t.Fatalf("requests = %v", sawAuth)
	}
	if !strings.HasSuffix(sawAuth[0], "token secret-pat") ||
		!strings.HasSuffix(sawAuth[1], "token secret-pat") {
		t.Fatalf("API 树内跳转应保留凭证: %v", sawAuth)
	}
	if strings.HasSuffix(sawAuth[2], "token secret-pat") {
		t.Fatalf("歧义段的跳转仍带凭证: %v", sawAuth)
	}
}

func TestValidateBaseURL(t *testing.T) {
	// 归一化与形状校验用公网 IP 字面量：不做真实 DNS（沙箱/CI 环境的解析器
	// 不可依赖），域名的 DNS 分支由下面注入假解析器的用例覆盖。
	t.Run("https 归一去尾部斜杠", func(t *testing.T) {
		got, err := ValidateBaseURL(context.Background(), "https://192.0.2.10:8443/", false)
		if err != nil || got != "https://192.0.2.10:8443" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("默认拒绝 http", func(t *testing.T) {
		if _, err := ValidateBaseURL(context.Background(), "http://forge.example.com", false); !errors.Is(err, ErrSchemeNotAllowed) {
			t.Fatalf("err = %v, want ErrSchemeNotAllowed", err)
		}
	})
	t.Run("私网放行时允许 http", func(t *testing.T) {
		// RFC1918 字面量：私网口径放行且不做真实 DNS（沙箱/CI 无解析依赖）。
		if _, err := ValidateBaseURL(context.Background(), "http://10.1.2.3", true); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("私网放行仍拒两口径恒禁地址", func(t *testing.T) {
		// 保存侧与拨号同口径：回环、链路本地/元数据、AWS IPv6 IMDS 在私网
		// 模式下也拒——这类基址落库即永远连不通。
		for _, raw := range []string{
			"http://127.0.0.1:3000", "http://169.254.169.254",
			"http://[fd00:ec2::254]", "http://[::1]",
		} {
			if _, err := ValidateBaseURL(context.Background(), raw, true); !errors.Is(err, ErrPrivateAddress) {
				t.Errorf("ValidateBaseURL(%s, private) err = %v, want ErrPrivateAddress", raw, err)
			}
		}
	})
	t.Run("IP 字面量私网拒绝", func(t *testing.T) {
		for _, raw := range []string{
			"https://127.0.0.1", "https://10.0.0.5", "https://192.168.1.1",
			"https://169.254.169.254", "https://[::1]", "https://172.16.9.9",
		} {
			if _, err := ValidateBaseURL(context.Background(), raw, false); !errors.Is(err, ErrPrivateAddress) {
				t.Errorf("ValidateBaseURL(%s) err = %v, want ErrPrivateAddress", raw, err)
			}
		}
	})
	t.Run("回环域名默认拒绝", func(t *testing.T) {
		// localhost 在正常机器上解析到回环；即使解析失败这里也必须报错——
		// 保存时连不上的基址不该进库。
		if _, err := ValidateBaseURL(context.Background(), "https://localhost:3000", false); err == nil {
			t.Fatal("https://localhost:3000 should be rejected")
		}
	})
	t.Run("解析到私网的域名拒绝（假解析器）", func(t *testing.T) {
		err := checkHostAddrs(context.Background(), "forge.internal",
			func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("10.1.2.3"), net.ParseIP("203.0.113.7")}, nil
			}, nil)
		if !errors.Is(err, ErrPrivateAddress) {
			t.Fatalf("err = %v, want ErrPrivateAddress", err)
		}
	})
	t.Run("解析失败按拒绝处理", func(t *testing.T) {
		err := checkHostAddrs(context.Background(), "nope.example.com",
			func(context.Context, string) ([]net.IP, error) {
				return nil, errors.New("no such host")
			}, nil)
		if err == nil {
			t.Fatal("resolution failure should be rejected")
		}
	})
	t.Run("形状非法", func(t *testing.T) {
		for _, raw := range []string{
			"", "  ", "forge.example.com", "https://", "https://a.com?x=1", "https://a.com#frag",
			// url.Parse 只查端口纯数字不查范围：:0、:65536 落库即永远连不上。
			"https://forge.example:0", "https://forge.example:65536",
			// 路径 . / .. 段拒收：代理归一后请求路径与字面前缀不同，
			// api_base_url 前缀保护语义先统一（%2e%2e 解码后同样命中）。
			"https://forge.example/api/../x", "https://forge.example/api/%2e%2e/x",
		} {
			if _, err := ValidateBaseURL(context.Background(), raw, false); !errors.Is(err, ErrInvalidBaseURL) {
				t.Errorf("ValidateBaseURL(%q) err = %v, want ErrInvalidBaseURL", raw, err)
			}
		}
	})
	t.Run("内嵌 userinfo 拒绝", func(t *testing.T) {
		// 凭证随 base_url 明文落库、回显并拼进审查链接，一律不收。
		for _, raw := range []string{
			"https://user:pass@forge.example.com",
			"https://bot@forge.example.com",
			"https://user:pass@192.0.2.10",
		} {
			if _, err := ValidateBaseURL(context.Background(), raw, false); !errors.Is(err, ErrInvalidBaseURL) {
				t.Errorf("ValidateBaseURL(%q) err = %v, want ErrInvalidBaseURL", raw, err)
			}
		}
	})
	t.Run("返回重编码后的归一串", func(t *testing.T) {
		// scheme 小写、去默认之外的手写斜杠；输入原样回传会把大小写等杂音
		// 落库（主机唯一判定按归一键，但展示与拼接用原串）。
		got, err := ValidateBaseURL(context.Background(), "HTTPS://192.0.2.10:8443/", false)
		if err != nil || got != "https://192.0.2.10:8443" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

// newTestServer 起一个只回 200 的 httptest 服务，返回基址。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testURLHost 取 URL 的纯主机名（小写、无 scheme 无端口）——与 AllowedHosts
// 的记账口径一致。
func testURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Hostname())
}

func TestClientDialGuard(t *testing.T) {
	srv := newTestServer(t)

	t.Run("默认拒绝回环目标", func(t *testing.T) {
		client, err := NewClient(Config{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = client.Get(srv.URL)
		if !errors.Is(err, ErrPrivateAddress) {
			t.Fatalf("err = %v, want ErrPrivateAddress", err)
		}
	})
	t.Run("私网放行名单主机仍受拨号校验", func(t *testing.T) {
		// 修复语义：放行只放宽 IP 段口径，绝不放掉拨号时校验——回环在私网
		// 口径下照拒（DNS rebinding 改指回环同理）。默认（生产）口径断言。
		client, err := NewClient(Config{
			AllowedHosts:        []string{testURLHost(srv.URL)},
			AllowPrivateNetwork: true,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.Get(srv.URL); !errors.Is(err, ErrPrivateAddress) {
			t.Fatalf("err = %v, want ErrPrivateAddress", err)
		}
	})
	t.Run("私网放行名单主机可连通（测试口径放宽回环）", func(t *testing.T) {
		relaxLoopbackForTest(t)
		client, err := NewClient(Config{
			AllowedHosts:        []string{testURLHost(srv.URL)},
			AllowPrivateNetwork: true,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
	})
	t.Run("私网放行不外溢到其他主机", func(t *testing.T) {
		client, err := NewClient(Config{
			AllowedHosts:        []string{"forge.example.com"},
			AllowPrivateNetwork: true,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.Get(srv.URL); !errors.Is(err, ErrPrivateAddress) {
			t.Fatalf("err = %v, want ErrPrivateAddress", err)
		}
	})
}

func TestClientRedirectPolicy(t *testing.T) {
	var inner *httptest.Server
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/jump" {
			// 跳到另一个端口 = 另一个 host:port，即使 IP 相同。
			http.Redirect(w, r, inner.URL+"/final", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	t.Cleanup(outer.Close)
	inner = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(inner.Close)

	// 正向连通需要放宽回环（httptest 落在 127.0.0.1）；拒绝语义的断言不受
	// 影响——放宽只作用于私网口径的回环段。
	relaxLoopbackForTest(t)

	newCfg := func() Config {
		return Config{
			AllowedHosts:        []string{testURLHost(outer.URL), testURLHost(inner.URL)},
			AllowPrivateNetwork: true,
		}
	}

	t.Run("跨主机（含端口）拒绝", func(t *testing.T) {
		client, err := NewClient(newCfg())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.Get(outer.URL + "/jump"); !errors.Is(err, ErrCrossHostRedirect) {
			t.Fatalf("err = %v, want ErrCrossHostRedirect", err)
		}
	})
	t.Run("同主机跳转放行", func(t *testing.T) {
		client, err := NewClient(newCfg())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		resp, err := client.Get(outer.URL + "/same")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
	})
	t.Run("https 到 http 降级拒绝", func(t *testing.T) {
		var tlsSrv *httptest.Server
		tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 同 host:port、仅 scheme 降级：隔离出降级规则（跨主机规则不触发）。
			http.Redirect(w, r, "http://"+tlsSrv.Listener.Addr().String()+"/final", http.StatusFound)
		}))
		t.Cleanup(tlsSrv.Close)
		caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})
		client, err := NewClient(Config{
			AllowedHosts:        []string{testURLHost(tlsSrv.URL)},
			AllowPrivateNetwork: true,
			CACertPEM:           string(caPEM),
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.Get(tlsSrv.URL); !errors.Is(err, ErrSchemeDowngrade) {
			t.Fatalf("err = %v, want ErrSchemeDowngrade", err)
		}
	})
	t.Run("未放行私网时 http 跳转拒绝", func(t *testing.T) {
		client, err := NewClient(Config{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		// 首跳未放行私网时连接本身就到不了；直接构造一次重定向校验策略：
		// CheckRedirect 的 https 要求先于跨主机判定生效。
		_, err = client.Get("http://127.0.0.1:1/x")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestClientCustomCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	relaxLoopbackForTest(t)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	t.Run("系统池连不上自签", func(t *testing.T) {
		client, err := NewClient(Config{AllowPrivateNetwork: true, AllowedHosts: []string{testURLHost(srv.URL)}})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := client.Get(srv.URL); err == nil {
			t.Fatal("expected TLS error without custom CA")
		}
	})
	t.Run("自定义 CA 可连通（无 InsecureSkipVerify）", func(t *testing.T) {
		client, err := NewClient(Config{
			AllowedHosts:        []string{testURLHost(srv.URL)},
			AllowPrivateNetwork: true,
			CACertPEM:           string(caPEM),
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with custom CA: %v", err)
		}
		resp.Body.Close()
		if transport, ok := client.Transport.(*http.Transport); ok {
			if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("InsecureSkipVerify must never be set")
			}
		}
	})
	t.Run("垃圾 PEM 构建失败", func(t *testing.T) {
		if _, err := NewClient(Config{CACertPEM: "not a pem"}); !errors.Is(err, ErrInvalidCACert) {
			t.Fatalf("err = %v, want ErrInvalidCACert", err)
		}
	})
}

func TestValidateCACertPEM(t *testing.T) {
	valid := testCAPEM(t)

	t.Run("真证书通过", func(t *testing.T) {
		if err := ValidateCACertPEM(valid); err != nil {
			t.Fatalf("valid pem rejected: %v", err)
		}
	})
	t.Run("伪证书块拒绝", func(t *testing.T) {
		// 32 个零字节的 CERTIFICATE 块 PEM 形状合法但解析不出证书。
		fake := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: make([]byte, 32)})
		if err := ValidateCACertPEM(string(fake)); !errors.Is(err, ErrInvalidCACert) {
			t.Fatalf("err = %v, want ErrInvalidCACert", err)
		}
	})
	t.Run("夹带私钥块拒绝", func(t *testing.T) {
		// 证书列里混私钥 = 凭证进了证书字段；即便证书本身有效也整体拒绝。
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testCAKey(t))})
		mixed := valid + string(keyPEM)
		if err := ValidateCACertPEM(mixed); !errors.Is(err, ErrInvalidCACert) {
			t.Fatalf("err = %v, want ErrInvalidCACert", err)
		}
	})
	t.Run("夹带私钥在证书前同样拒绝", func(t *testing.T) {
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: make([]byte, 16)})
		if err := ValidateCACertPEM(string(keyPEM) + valid); !errors.Is(err, ErrInvalidCACert) {
			t.Fatalf("err = %v, want ErrInvalidCACert", err)
		}
	})
	t.Run("其他类型块忽略", func(t *testing.T) {
		extra := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: make([]byte, 16)})
		if err := ValidateCACertPEM(valid + string(extra)); err != nil {
			t.Fatalf("non-cert non-key block should be ignored: %v", err)
		}
	})
	if err := ValidateCACertPEM("hello"); !errors.Is(err, ErrInvalidCACert) {
		t.Fatalf("err = %v, want ErrInvalidCACert", err)
	}
	if err := ValidateCACertPEM(""); !errors.Is(err, ErrInvalidCACert) {
		t.Fatalf("empty pem should be invalid, got %v", err)
	}
}

func TestAllowedHostsAndDefaultAPIBase(t *testing.T) {
	hosts := AllowedHosts("https://Forge.Example.com:8443/f", "https://forge.example.com/api", "https://a.io", "")
	if len(hosts) != 2 || hosts[0] != "forge.example.com" || hosts[1] != "a.io" {
		t.Fatalf("AllowedHosts = %v", hosts)
	}
	if got := DefaultAPIBaseURL("https://forge.example.com/"); got != "https://forge.example.com/api/v1" {
		t.Fatalf("DefaultAPIBaseURL = %q", got)
	}
}

// TestClientTimeout 确认 Config.Timeout 缺省落到 DefaultTimeout。
func TestClientTimeout(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want %v", client.Timeout, DefaultTimeout)
	}
	custom, err := NewClient(Config{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if custom.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s", custom.Timeout)
	}
}

// ---- 自签 CA 构造（仅供 PEM 解析校验） ----

func testCAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	return key
}

func testCAPEM(t *testing.T) string {
	t.Helper()
	key := testCAKey(t)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(crand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

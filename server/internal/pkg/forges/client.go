// Package forges 为代码仓集成（INT-166）构建 SSRF 防护的 HTTP client：按集成
// 配置（基址、自定义 CA、私网放行开关）逐实例构建，拨号时按实际解析 IP 校验
// （防 DNS rebinding），重定向禁止跨主机。webhook 出站与轮询（P1）复用同一
// 构建入口，不另起第二套防护。
package forges

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout 单请求兜底超时（连接检测同步等待，不能挂太久）。
const DefaultTimeout = 10 * time.Second

var (
	// ErrPrivateAddress 目标解析到内网/保留地址（含元数据 169.254.169.254）。
	// 未开启 allow_private_network 的集成一律拒绝；开启后名单主机的拨号按
	// 私网口径放宽（见 isBlockedPrivateMode），回环与链路本地仍拒。
	ErrPrivateAddress = errors.New("目标地址解析到内网或保留地址，已拒绝连接")
	// ErrCrossHostRedirect 重定向跨主机：集成 client 只跟随同主机跳转。
	ErrCrossHostRedirect = errors.New("重定向跨主机，已拒绝跟随")
	// ErrSchemeDowngrade 重定向从 https 降到 http：即使私网放行开启也拒绝——
	// http 只该用于配置的明文基址，不该被重定向悄悄带出来。
	ErrSchemeDowngrade = errors.New("重定向从 https 降到 http，已拒绝跟随")
	// ErrSchemeNotAllowed http 仅在 allow_private_network 且该集成开启时可用。
	ErrSchemeNotAllowed = errors.New("该集成仅允许 https（开启私网放行后才能用 http）")
	// ErrInvalidBaseURL 基址不是合法的绝对 URL、缺 host，或内嵌了 userinfo
	// （凭证随 base_url 明文落库回显，一律不收）。
	ErrInvalidBaseURL = errors.New("基址必须是带 host 的绝对 URL")
	// ErrInvalidCACert 自定义 CA 不是可解析的 PEM 证书（含私钥块、证书块解不
	// 开、没有一个 CERTIFICATE 块）。
	ErrInvalidCACert = errors.New("CA 证书不是有效的 PEM")
)

// 拨号判定的两个口径入口。包级变量仅为测试可注入——httptest 只能落在回环
// 上，正向端到端用例临时放宽私网口径；默认值永远是生产口径，「每次拨号都过
// Control 校验」的防护结构不在注入面里。
var (
	strictBlocked      = isBlockedIP
	privateModeBlocked = isBlockedPrivateMode
)

// Config 按集成构建 client 的全部参数。字段都是该集成的落库配置投影；
// client 本身无状态，每次使用现建（连接检测频率低，不值得缓存）。
type Config struct {
	// AllowedHosts 本集成配置主机（base_url / api_base_url 的 host，小写、
	// 不带端口）。allow_private_network=true 时只有这些主机的拨号免做私网
	// IP 校验——开关放行的是「这个 Forgejo 在内网」，不是「随便连内网」。
	AllowedHosts []string
	// CACertPEM 自定义 CA（PEM）；空串 = 仅系统根证书池。追加而非替换，
	// 公网签发的证书照常校验。
	CACertPEM string
	// AllowPrivateNetwork 放行配置主机解析到私网地址；http 同样只在开启时可用。
	AllowPrivateNetwork bool
	// Timeout 单请求超时；0 取 DefaultTimeout。
	Timeout time.Duration
}

// NewClient 按集成配置构建 *http.Client。CA 解析失败返回 ErrInvalidCACert；
// 其余配置错误在保存时已被 ValidateBaseURL 拦下，这里不再重复校验。
func NewClient(cfg Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 必须自己拨到目标才能校验目标 IP：走环境代理时校验到的会是代理地址
	// （公网代理→防线失效，回环代理→全部误拒）。代价是集成请求不支持
	// HTTPS_PROXY 等环境代理。
	transport.Proxy = nil

	if cfg.CACertPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			// 系统池读取失败（极少数精简系统）退回空池：自定义 CA 仍可用，
			// 只是系统签发的证书不再被信任，不静默放开校验。
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, ErrInvalidCACert
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.RootCAs = pool
	}

	allowed := allowedHostSet(cfg.AllowedHosts)
	// 两个拨号器都挂 Control：私网放行只是放宽 IP 段口径（allowed 主机走
	// privateModeBlocked，其余走公网口径），绝不放掉「拨号时校验」这道防线——
	// 保存后 DNS 改指、重定向后的再连接照样被拦。
	guarded := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   dialGuard(strictBlocked),
	}
	transport.DialContext = guarded.DialContext
	if cfg.AllowPrivateNetwork {
		if len(allowed) == 0 {
			// 开关开了却没有可识别的配置主机：放行失去对象，按未开启处理，
			// 宁可错拒也不把开关退化成「任意私网通行证」。
		} else {
			// 放行粒度仍是主机名：判定用 DialContext 收到的 addr 主机名（DNS
			// 解析之前）决定口径，解析出的 IP 再按私网口径逐次校验。
			relaxed := &net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
				Control:   dialGuard(privateModeBlocked),
			}
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if host, _, err := net.SplitHostPort(addr); err == nil {
					if _, ok := allowed[strings.ToLower(host)]; ok {
						return relaxed.DialContext(ctx, network, addr)
					}
				}
				return guarded.DialContext(ctx, network, addr)
			}
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			if !cfg.AllowPrivateNetwork && req.URL.Scheme != "https" {
				return ErrSchemeNotAllowed
			}
			// https→http 降级一律拒绝：即使私网放行开着（明文 http 只该来自
			// 管理员配置的基址，不该被响应悄悄带出来）。
			if via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
				return ErrSchemeDowngrade
			}
			// 只跟随同主机跳转：Forgejo 反代可能把 / 跳到登录页，但不会跨到
			// 别的主机；跨主机在集成场景里就是 SSRF 或劫持信号。
			if req.URL.Host != via[0].URL.Host {
				return ErrCrossHostRedirect
			}
			return nil
		},
	}, nil
}

// dialGuard 生成 net.Dialer.Control：在 DNS 解析之后、建连之前判定实际目标
// 地址（address 已是「IP:port」），每次建连——含重定向后的再连接、DNS
// rebinding 后的第二次解析——都会被拦一遍。blocked 决定 IP 段口径。
func dialGuard(blocked func(net.IP) bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("解析目标地址 %q 失败: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("目标地址 %q 不是合法 IP", host)
		}
		if blocked(ip) {
			return ErrPrivateAddress
		}
		return nil
	}
}

// saveResolveTimeout 保存时 DNS 校验的解析上限：调用方 ctx（管理端请求）可能
// 远超请求生命周期才到期，解析器卡顿（NS 黑洞、串行重试）不能把 handler 协程
// 一直挂在 LookupIPAddr 上。
const saveResolveTimeout = 5 * time.Second

// ValidateBaseURL 校验并归一集成基址：https 必需（allow_private_network 时
// 该集成可用 http）、host 非空、不带 userinfo/query/fragment、去尾部 /；并做
// 一次保存时 DNS 校验（解析逐 IP 过 blocked 口径，解析失败按拒绝处理）。
// allow_private_network=true 用私网放行口径而非跳过校验：RFC1918/ULA/CGNAT
// 放行，但回环、链路本地/元数据、保留段两口径恒拒——http://127.0.0.1 这类
// 「落库即永远连不通」的基址在保存时就挡掉。返回归一后的基址。
func ValidateBaseURL(ctx context.Context, raw string, allowPrivateNetwork bool) (string, error) {
	u, err := parseBaseURL(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" && !(allowPrivateNetwork && u.Scheme == "http") {
		return "", ErrSchemeNotAllowed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolveCtx, cancel := context.WithTimeout(ctx, saveResolveTimeout)
	defer cancel()
	blocked := isBlockedIP
	if allowPrivateNetwork {
		blocked = privateModeBlocked
	}
	if err := checkHostAddrs(resolveCtx, u.Hostname(), nil, blocked); err != nil {
		return "", err
	}
	// 回传重编码后的 URL 而不是原始输入：percent 编码、默认端口等由 url 包
	// 口径统一，杜绝「输入里夹带的东西原样落库」。
	return strings.TrimRight(u.String(), "/"), nil
}

// ValidateCACertPEM 检查 PEM 内容：迭代全部块——出现私钥块（Type 含
// PRIVATE KEY）即拒绝（凭证不该混进证书列）；每个 CERTIFICATE 块必须能
// x509 解析（32 字节零块之类的伪证书挡在这里）；至少一个 CERTIFICATE 块；
// 其他类型的块忽略。
func ValidateCACertPEM(pemStr string) error {
	rest := []byte(pemStr)
	sawCert := false
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return ErrInvalidCACert
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return ErrInvalidCACert
		}
		sawCert = true
	}
	if !sawCert {
		return ErrInvalidCACert
	}
	return nil
}

// DefaultAPIBaseURL 由集成基址推导 API 基址（Forgejo/Gitea 惯例）。
func DefaultAPIBaseURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/v1"
}

// HostKey 集成基址的归一键：小写 host + 端口，空端口按 scheme 缺省补齐
// （https→443、http→80）。解析失败或缺 host 返回空串（保存时已校验过，空键
// 只防御存量脏行，调用方把它排除在比较与唯一约束之外）。同主机查重、项目侧
// 仓库 URL 匹配与启动迁移的 host_key 回填（forge_integrations.host_key）共用
// 这一个实现。
func HostKey(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		return host + ":" + port
	}
	if strings.EqualFold(u.Scheme, "http") {
		return host + ":80"
	}
	return host + ":443"
}

// AllowedHosts 从一组基址提取 host 记账（小写、去端口、去空白、去重、按输入
// 顺序），供 Config.AllowedHosts 使用。解析失败的条目跳过——传入的都是保存时
// 校验过的基址，这里只是防御。
func AllowedHosts(baseURLs ...string) []string {
	seen := map[string]struct{}{}
	var hosts []string
	for _, raw := range baseURLs {
		u, err := parseBaseURL(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == "" {
			continue
		}
		if _, dup := seen[host]; dup {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

// allowedHostSet 规整成小写主机名集合：与 AllowedHosts 相同的口径，NewClient
// 每次现算，调用方不需要预先归一。
func allowedHostSet(hosts []string) map[string]struct{} {
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = struct{}{}
		}
	}
	return set
}

// parseBaseURL 解析并做形状校验：绝对 URL、host 非空、不带 userinfo、query
// 或 fragment。
func parseBaseURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, ErrInvalidBaseURL
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, ErrInvalidBaseURL
	}
	// userinfo 拒收：凭证随 base_url 明文落库、回显并被拼进审查链接。
	if u.User != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalidBaseURL
	}
	// url.Parse 只查端口是否纯数字，不查范围——:0、:65536 能落库但永远连
	// 不上。保存时把超出 TCP 端口的写法挡掉。
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, ErrInvalidBaseURL
		}
	}
	return u, nil
}

// resolveHostIPs 保存时 DNS 校验的解析入口；包级变量只为测试可注入，生产
// 走 net.DefaultResolver。拨号路径始终用真实解析——防护对象就是真实连接。
var resolveHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

// checkHostAddrs 解析主机名并逐 IP 校验；任一 IP 落在拒绝段即拒绝（域名指向
// 多个地址时，放行意味着客户端可能连到被拒的那个）。解析失败按拒绝处理——
// 保存时连不上的基址不该进库。lookup 为 nil 用 resolveHostIPs；blocked 为
// nil 用公网口径 isBlockedIP，私网放行场景传 privateModeBlocked。
func checkHostAddrs(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error), blocked func(net.IP) bool) error {
	if host == "" {
		return ErrInvalidBaseURL
	}
	if blocked == nil {
		blocked = isBlockedIP
	}
	if ip := net.ParseIP(host); ip != nil {
		if blocked(ip) {
			return ErrPrivateAddress
		}
		return nil
	}
	resolve := lookup
	if resolve == nil {
		resolve = resolveHostIPs
	}
	addrs, err := resolve(ctx, host)
	if err != nil {
		return fmt.Errorf("解析主机 %q 失败: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("主机 %q 没有可用的解析结果", host)
	}
	for _, ip := range addrs {
		if blocked(ip) {
			return ErrPrivateAddress
		}
	}
	return nil
}

// isBlockedIP SSRF 地址判定的公网口径：拒绝回环、RFC1918 私网、ULA fc00::/7、
// 链路本地 fe80::/10 与 169.254.0.0/16（云元数据 169.254.169.254 在其中）、
// 多播、未指定地址，以及 CGNAT 100.64.0.0/10、0.0.0.0/8、192.0.0.0/24（IETF
// 协议分配）、192.88.99.0/24（已废弃 6to4 relay anycast）、198.18.0.0/15
// （基准测试）、240.0.0.0/4（保留段）。
func isBlockedIP(ip net.IP) bool {
	return blockedByPolicy(ip, true)
}

// isBlockedPrivateMode 私网放行口径（allow_private_network 开启时名单主机的
// 拨号判定）：放行 RFC1918、ULA fc00::/7、CGNAT 100.64.0.0/10 与公网——放行
// 的是「这个 Forgejo 在内网」；但回环、链路本地（含元数据）、多播、未指定、
// 0.0.0.0/8、192.0.0.0/24、192.88.99.0/24、198.18.0.0/15、240.0.0.0/4 与已
// 废弃 fec0::/10 无论开关如何都拒绝。IPv6 内嵌 IPv4（mapped/6to4/Teredo/
// NAT64）按同一口径递归判定，否则「看起来是公网」的 IPv6 就绕过了判定。
func isBlockedPrivateMode(ip net.IP) bool {
	return blockedByPolicy(ip, false)
}

// awsIMDSv6Endpoint AWS IMDS 的 IPv6 端点（EC2 实例元数据服务在 IPv6-only
// 实例上的固定地址）。它落在 ULA fd00::/8 里，私网放行口径会把它当「内网
// Forgejo」放过去——但那是元数据服务，偷到凭证等于整个云账号失守，两口径
// 一律精确拒绝。
var awsIMDSv6Endpoint = net.ParseIP("fd00:ec2::254")

// blockedByPolicy 地址段判定核心。strict=true 是公网口径（一切私网/保留段
// 拒绝），false 是私网放行口径（只拒绝放了也危险的段）。
func blockedByPolicy(ip net.IP, strict bool) bool {
	if ip == nil {
		return true
	}
	// 回环、链路本地（169.254.0.0/16 含云元数据、fe80::/10）、多播、未指定
	// 两个口径都拒绝。
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// RFC1918（IsPrivate 对 IPv4 即私网三段）与 CGNAT 只在公网口径拒绝；
		// 私网放行恰恰是放行它们。169.254.0.0/16 已由 IsLinkLocalUnicast 覆盖。
		if strict && (ip.IsPrivate() || isCGNAT(ip4)) {
			return true
		}
		return isAlwaysBlockedV4(ip4)
	}
	ipv6 := ip.To16()
	if ipv6 == nil {
		return true
	}
	// ULA fc00::/7（IsPrivate 对 IPv6 即 ULA）只在公网口径拒绝。
	if strict && ip.IsPrivate() {
		return true
	}
	// AWS IMDS 的 IPv6 端点：ULA 段放不拦的例外，精确匹配（相邻 ULA 如
	// fd00:ec2::1 不受影响）。
	if ip.Equal(awsIMDSv6Endpoint) {
		return true
	}
	// fec0::/10 已废弃的 site-local（RFC 3879）：第二字节高两位为 11，
	// 与 fe80::/10（10）区分；两个口径都拒。
	if ipv6[0] == 0xfe && ipv6[1]&0xc0 == 0xc0 {
		return true
	}
	// NAT64：只有 well-known 前缀 64:ff9b::/96（bytes 4-11 全零）按末 4 字节
	// 解出内嵌 IPv4 递归判定（同一口径——私网放行时载荷是 RFC1918 的 Forgejo
	// 可达）；local-use 前缀 64:ff9a::/96 不是公网可路由空间，公网 Forgejo
	// 不会出现——整体拒绝，宁拒不放。
	if ipv6[0] == 0 && ipv6[1] == 0x64 && ipv6[2] == 0xff {
		switch ipv6[3] {
		case 0x9b:
			if allZero(ipv6[4:12]) {
				return blockedByPolicy(net.IP(append([]byte{}, ipv6[12:16]...)), strict)
			}
			return true
		case 0x9a:
			return true
		}
	}
	// 6to4 2002::/16：内嵌 IPv4 在第 2-5 字节。
	if ipv6[0] == 0x20 && ipv6[1] == 0x02 {
		return blockedByPolicy(net.IP(append([]byte{}, ipv6[2:6]...)), strict)
	}
	// Teredo 2001::/32：客户端 IPv4 在末 4 字节、按位取反。
	if ipv6[0] == 0x20 && ipv6[1] == 0x01 && allZero(ipv6[2:4]) {
		v4 := net.IP(append([]byte{}, ipv6[12:16]...))
		for i := range v4 {
			v4[i] = ^v4[i]
		}
		return blockedByPolicy(v4, strict)
	}
	return false
}

// isCGNAT 100.64.0.0/10 运营商级 NAT 段（Tailscale 等组网也落在其中）。
func isCGNAT(ip4 net.IP) bool {
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] < 128
}

// isAlwaysBlockedV4 两个口径都拒绝的 IPv4 保留段（RFC1918/CGNAT 的口径差异
// 在 blockedByPolicy 里先行判定）。
func isAlwaysBlockedV4(ip4 net.IP) bool {
	switch {
	case ip4[0] == 0: // 0.0.0.0/8
		return true
	case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF 协议分配
		return true
	case ip4[0] == 192 && ip4[1] == 88 && ip4[2] == 99: // 192.88.99.0/24 已废弃 anycast
		return true
	case ip4[0] == 198 && ip4[1] >= 18 && ip4[1] <= 19: // 198.18.0.0/15 基准测试
		return true
	case ip4[0] >= 240: // 240.0.0.0/4 保留（含广播）
		return true
	}
	return false
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

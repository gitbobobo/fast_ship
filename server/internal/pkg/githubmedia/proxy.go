package githubmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const ProxyPath = "/api/github/media-proxy"

const (
	defaultCacheSubdir     = "github-media-cache"
	defaultCacheTTL        = 24 * time.Hour
	defaultMaxCacheBytes   = 256 << 20
	defaultRequestTimeout  = 45 * time.Second
	defaultTempFilePattern = "media-*.tmp"
)

var (
	// 错误文案会原样落到 review_assets.error 与 status 描述里，写成用户能
	// 看懂的中文原因；/api/github/media-proxy 的对外文案由 handler 固定生成，
	// 不经过这里。
	ErrInvalidMediaURL          = errors.New("不支持的图片地址")
	ErrUnsupportedUpstreamMedia = errors.New("上游返回的不是图片")
	ErrBlockedUpstreamAddress   = errors.New("图片地址指向内网或保留地址")
	ErrEmptyUpstreamMedia       = errors.New("上游返回空内容")
	ErrMediaTooLarge            = errors.New("图片超过大小上限")
	// ErrLocalCache 本地截图缓存操作失败（建缓存目录/建临时文件/写盘/改名/
	// 读缓存元数据）。在产生点打标：service 层只认这个 sentinel 归「截图缓
	// 存写入失败」类，不再嗅探 *fs.PathError / *os.SyscallError 外壳——网络
	// 错误的 syscall 形态（dial connect: connection refused 也是 SyscallError）
	// 不会再被误归成磁盘故障。
	ErrLocalCache = errors.New("本地截图缓存操作失败")
)

// localCacheError 本地缓存文件操作的统一包装：errors.Is 同时命中
// ErrLocalCache（调用方分类用）与原始错误链（fs.ErrNotExist、syscall.ENOSPC
// 等原判定不丢）。
type localCacheError struct{ err error }

func (e *localCacheError) Error() string { return ErrLocalCache.Error() + ": " + e.err.Error() }

// Unwrap 双链：sentinel 与原始错误都要能被 errors.Is 找到。
func (e *localCacheError) Unwrap() []error { return []error{ErrLocalCache, e.err} }

// markLocalCacheErr 给本地缓存操作失败包上 ErrLocalCache；nil 原样返回。
func markLocalCacheErr(err error) error {
	if err == nil {
		return nil
	}
	return &localCacheError{err: err}
}

// cacheFileWriter 写侧打标：io.Copy 的失败可能来自读网络 body（网络错误，
// 原样外抛）或写本地临时文件（缓存故障）。包装后的 dst 不再实现 ReaderFrom
// （*os.File 在 Linux 上的 splice/copy_file_range 快路径因此不会旁路这里），
// 写失败必然经过 Write 被打上 ErrLocalCache；短写（写方返回 n < len(p) 且无
// 错误）同样在 Write 里打标——io.Copy 会自行把它折成 io.ErrShortWrite 返回，
// 不经过下面的 err 分支。
type cacheFileWriter struct{ w io.Writer }

func (c cacheFileWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		return n, &localCacheError{err: err}
	}
	if n < len(p) {
		// 短写（磁盘满/配额耗尽时部分文件系统先返回 n < len(p) 且无错误）：
		// io.Copy 对此自行返回 io.ErrShortWrite，不走错误包装——在这里补
		// 打标，短写才落到 ErrLocalCache 的「截图缓存写入失败」类，而不是
		// service 层兜底「无法连接图片服务器」把磁盘故障说成网络故障。
		return n, &localCacheError{err: io.ErrShortWrite}
	}
	return n, nil
}

type UpstreamStatusError struct {
	StatusCode int
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("上游返回状态码 %d", e.StatusCode)
}

type cacheEntry struct {
	URL          string    `json:"url"`
	ContentType  string    `json:"content_type"`
	CacheControl string    `json:"cache_control"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Size         int64     `json:"size"`
	CachedAt     time.Time `json:"cached_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	// Scope 写入该条目所用的授权域（如安装 id）；空串 = 匿名拉取的公开内容。
	// 带 scope 的条目只对同 scope 的请求命中——私有附件被某安装令牌拉进缓存
	// 后，匿名或其他安装的请求不得直接命中（否则知道 URL 就能跨安装取到私
	// 有内容）。无此字段的旧条目不可信（来源不可辨），服务启动时由
	// purgeLegacyCacheEntries 一次性清掉。
	Scope string `json:"scope,omitempty"`
}

func (e cacheEntry) expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt)
}

type CachedMedia struct {
	Metadata cacheEntry
	Path     string
}

// CachedImage 用给定元数据构造 CachedMedia。cacheEntry 未导出，service 层的
// 测试桩无法直接填 Metadata，统一走这个构造（仅测试与内部装配用，别扩大）。
func CachedImage(path, contentType string, size int64) *CachedMedia {
	return &CachedMedia{
		Metadata: cacheEntry{ContentType: contentType, Size: size},
		Path:     path,
	}
}

type PassThroughResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type ProxyService struct {
	client            *http.Client
	cacheDir          string
	cacheTTL          time.Duration
	maxCacheEntrySize int64
	allowURL          func(*url.URL) bool
	// requireHTTPS 只接受 https 上游（截图面）；imagesOnly 只收图片内容。
	// GitHub 媒体面两者全关：白名单语义 + 图片视频都收，与截图面是两套准入。
	requireHTTPS bool
	imagesOnly   bool

	mu sync.Mutex
	// download 每 URL 一把容量 1 的信号量（发送即持有、接收即释放）：同 URL
	// 下载串行，但等待必须吃 ctx——sync.Mutex 锁不来取消，30s 探测时限会被
	// 同 URL 前一请求的 45s 客户端超时拖穿。
	download map[string]chan struct{}
}

// serviceConfig newService 的装配参数；零值字段按 GitHub 媒体代理语义兜底。
type serviceConfig struct {
	cacheSubdir       string
	cacheTTL          time.Duration
	maxCacheEntrySize int64
	client            *http.Client
	allowURL          func(*url.URL) bool
	requireHTTPS      bool
	imagesOnly        bool
	guardAddress      bool
}

// NewProxyService GitHub 媒体代理面：GitHub 域名白名单、图片视频都收，
// 服务 /api/github/media-proxy。
func NewProxyService(cacheRoot string) *ProxyService {
	return newService(cacheRoot, serviceConfig{
		cacheSubdir: defaultCacheSubdir,
		allowURL:    isAllowedGitHubMediaURL,
	})
}

// NewReviewMediaService PR 审查截图下载面：放行任意公网 https 图片，SSRF
// 防线不在域名白名单，而在拨号时校验解析出的 IP——重定向与 DNS 重绑定
// 的每次建连都会被拦。与 NewProxyService 是两个独立实例，别混用。
func NewReviewMediaService(cacheRoot string) *ProxyService {
	return newService(cacheRoot, serviceConfig{
		cacheSubdir:  "review-screenshot-cache",
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
		imagesOnly:   true,
		guardAddress: true,
	})
}

// allowPublicImageHost 截图面的 host 准入：任意非空 host 即放行（真正的
// SSRF 防线在 guardAddress 的拨号校验）。
func allowPublicImageHost(parsed *url.URL) bool {
	return parsed != nil && strings.TrimSpace(parsed.Hostname()) != ""
}

func newService(cacheRoot string, cfg serviceConfig) *ProxyService {
	if cfg.cacheSubdir == "" {
		cfg.cacheSubdir = defaultCacheSubdir
	}
	if cfg.cacheTTL <= 0 {
		cfg.cacheTTL = defaultCacheTTL
	}
	if cfg.maxCacheEntrySize <= 0 {
		cfg.maxCacheEntrySize = defaultMaxCacheBytes
	}
	if cfg.allowURL == nil {
		cfg.allowURL = isAllowedGitHubMediaURL
	}
	if cfg.client == nil {
		cfg.client = newGuardedClient(cfg.requireHTTPS, cfg.allowURL, cfg.guardAddress)
	}
	s := &ProxyService{
		client:            cfg.client,
		cacheDir:          filepath.Join(cacheRoot, cfg.cacheSubdir),
		cacheTTL:          cfg.cacheTTL,
		maxCacheEntrySize: cfg.maxCacheEntrySize,
		allowURL:          cfg.allowURL,
		requireHTTPS:      cfg.requireHTTPS,
		imagesOnly:        cfg.imagesOnly,
		download:          make(map[string]chan struct{}),
	}
	s.purgeLegacyCacheEntries()
	return s
}

// legacyScopeMarker scope 进缓存键之前的存量条目不可信标记：老条目的 meta
// 没有 Scope 字段，无法区分是匿名拉取的公开内容还是安装令牌拉取的私有附
// 件——后者若继续按匿名键命中，知道 URL 就能经 /api/github/media-proxy 跨
// 安装取到私有内容。构造服务时清一次：标记缺失则删光目录下的缓存文件，
// 公开内容重拉即可，私有内容宁可重拉不可误给。
const legacyScopeMarker = ".cache-scope-v2"

func (s *ProxyService) purgeLegacyCacheEntries() {
	if _, err := os.Stat(filepath.Join(s.cacheDir, legacyScopeMarker)); err == nil {
		return
	}
	if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
		return
	}
	if entries, err := os.ReadDir(s.cacheDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".json") {
				_ = os.Remove(filepath.Join(s.cacheDir, name))
			}
		}
	}
	// 标记写失败也接受：下次启动重清一遍空目录而已，不会因此泄漏。
	_ = os.WriteFile(filepath.Join(s.cacheDir, legacyScopeMarker), []byte("scope-v2\n"), 0o640)
}

// newProxyService 旧内部构造（GitHub 媒体面语义：允许 http/https、图片视频
// 都收、不拨号校验），保留给现有测试。
func newProxyService(cacheRoot string, cacheTTL time.Duration, maxCacheEntrySize int64, client *http.Client, allowURL func(*url.URL) bool) *ProxyService {
	return newService(cacheRoot, serviceConfig{
		cacheSubdir:       defaultCacheSubdir,
		cacheTTL:          cacheTTL,
		maxCacheEntrySize: maxCacheEntrySize,
		client:            client,
		allowURL:          allowURL,
	})
}

// newGuardedClient 默认客户端：重定向按实例自身的 allowURL/requireHTTPS 复检
// （旧实现写死 GitHub 白名单，截图面会拦错/放错）；guardAddress 时在拨号
// 前校验解析 IP。Transport 克隆自 DefaultTransport，保留连接池参数。
func newGuardedClient(requireHTTPS bool, allowURL func(*url.URL) bool, guardAddress bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if guardAddress {
		// 必须自己拨到目标才能校验目标 IP：走环境代理时校验到的会是代理地址
		// （公网代理→防线失效，回环代理→全部误拒）。代价是这条下载面不支持
		// HTTPS_PROXY 等环境代理；GitHub 媒体面不 guard，仍走环境代理。
		transport.Proxy = nil
		transport.DialContext = (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   guardDialControl,
		}).DialContext
	}
	return &http.Client{
		Timeout:   defaultRequestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			if requireHTTPS && req.URL.Scheme != "https" {
				return ErrInvalidMediaURL
			}
			if !allowURL(req.URL) {
				return ErrInvalidMediaURL
			}
			return nil
		},
	}
}

// guardDialControl 拨号前校验已解析的上游地址。net.Dialer.Control 在 DNS
// 解析之后、建连之前被调用（address 已是「IP:port」），因此每次建连——
// 含重定向后的再连接、DNS 重绑定后的第二次解析——都会被这里拦一遍。
func guardDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("解析上游地址 %q 失败: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("上游地址 %q 不是合法 IP", host)
	}
	if isBlockedIP(ip) {
		return ErrBlockedUpstreamAddress
	}
	return nil
}

// isBlockedIP SSRF 地址判定：拒绝回环、RFC1918 私网、IPv6 ULA/链路本地/
// 多播/未指定/已废弃 site-local（fec0::/10，见下）地址，以及 169.254.0.0/16、
// 100.64.0.0/10（CGNAT）、0.0.0.0/8、192.0.0.0/24（IETF 协议分配，含
// .9/.10 anycast）、192.88.99.0/24（已废弃 6to4 relay anycast）、
// 198.18.0.0/15（基准测试）、240.0.0.0/4（保留段，含广播）地址；文档示例
// 网段（192.0.2.0/24、198.51.100.0/24、203.0.113.0/24）不在此列——它们是
// 公网可路由空间里的教学地址，有意放行。IPv6 还要解出内嵌 IPv4 再判一遍
// （NAT64 只有 well-known /96 解码，/32 内其余前缀长度与 local-use /96 整体
// 拒绝，见下）；公网地址放行。
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 169.254.0.0/16 已由 IsLinkLocalUnicast 覆盖；下面几段标准判定没有。
		switch {
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] < 128: // 100.64.0.0/10 CGNAT
			return true
		case ip4[0] == 0: // 0.0.0.0/8
			return true
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF 协议分配（含 .9/.10 anycast）
			return true
		case ip4[0] == 192 && ip4[1] == 88 && ip4[2] == 99: // 192.88.99.0/24 已废弃 6to4 relay anycast
			return true
		case ip4[0] == 198 && ip4[1] >= 18 && ip4[1] <= 19: // 198.18.0.0/15 基准测试
			return true
		case ip4[0] >= 240: // 240.0.0.0/4 保留（含广播 255.255.255.255）
			return true
		}
		return false
	}
	// fec0::/10 已废弃的 site-local（RFC 3879）整体拒绝：标准判定里没有这段
	// ——IsPrivate 只认 fc00::/7 ULA，IsLinkLocalUnicast 只认 fe80::/10——
	// 旧实现会当公网放行。与 fe80::/10 的区别在第二字节高两位（11 vs 10）。
	if ipv6 := ip.To16(); ipv6 != nil && ipv6[0] == 0xfe && ipv6[1]&0xc0 == 0xc0 {
		return true
	}
	// NAT64/6to4/Teredo 等前缀本身是公网单播，能内嵌任意 IPv4——载荷是
	// 内网地址时同样要拒，否则「看起来是公网」的 IPv6 就绕过了上面的判定。
	// 64:ff9a::/96（RFC 8215 local-use）整体拒绝：布局与 well-known /96 相同
	// （bytes 4-11 全零、内嵌 IPv4 恰在末 4 字节），按解码规则「解得出」，但
	// 它与 64:ff9b::/32 一样是 local-use 前缀，不属于公网可路由空间，公网图
	// 床不会出现——不为它维持一条解码例外，宁拒不放。
	// NAT64（64:ff9b::/32，RFC 6052/8215）统一成一条规则：只有 well-known
	// /96（bytes 4-11 全零）按末 4 字节解出内嵌 IPv4 递归判定（内嵌公网
	// 放行）；/32 内其余前缀长度——RFC 6052 §2.2 的 /40 /48 /56 /64 布局
	// （IPv4 高低 16 位被 u 八位组隔开、位置随前缀长度变化，按任何固定偏移
	// 都解不出可靠的内嵌 IPv4）与 RFC 8215 的 local-use /48（布局还可由
	// 网络自行选择）——一律整体拒绝。这些前缀不该出现在公网图床上，宁可
	// 错拒不可放过，有意保守。
	if ipv6 := ip.To16(); ipv6 != nil {
		if isLocalUseNAT64(ipv6) {
			return true
		}
		if isNAT64Prefix(ipv6) {
			if allZero(ipv6[4:12]) {
				if v4 := embeddedIPv4(ip); v4 != nil && isBlockedIP(v4) {
					return true
				}
				return false
			}
			return true
		}
	}
	if v4 := embeddedIPv4(ip); v4 != nil && isBlockedIP(v4) {
		return true
	}
	return false
}

// isNAT64Prefix 地址（16 字节形式）是否落在 NAT64 前缀 64:ff9b::/32 内。
func isNAT64Prefix(ipv6 []byte) bool {
	return ipv6[0] == 0 && ipv6[1] == 0x64 && ipv6[2] == 0xff && ipv6[3] == 0x9b
}

// isLocalUseNAT64 地址（16 字节形式）是否落在 RFC 8215 local-use NAT64 前缀
// 64:ff9a::/96 内（前 4 字节 00 64 ff 9a，与 well-known /32 只差末字节）。
func isLocalUseNAT64(ipv6 []byte) bool {
	return ipv6[0] == 0 && ipv6[1] == 0x64 && ipv6[2] == 0xff && ipv6[3] == 0x9a
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// embeddedIPv4 解出 IPv6 地址里内嵌的 IPv4；不是内嵌形态返回 nil。解出的
// 地址仍要过一遍 isBlockedIP（递归到此为止：IPv4 不会再内嵌）。
//
// 各前缀的内嵌位置（16 字节形式，下标从 0 起，这是本函数唯一的正确性依据）：
//   - 2001:0000::/32 Teredo（RFC 4380）：0-3 前缀、4-7 服务器 IPv4、8-9
//     flags、10-11 混淆 UDP 端口、12-15 客户端 IPv4。客户端 IPv4 按位取反
//     存储（混淆字段，防止中间设备按内容过滤），取反回来才是真实地址。
//   - 2002::/16 6to4（RFC 3056）：内嵌 IPv4 在 2-5。
//   - 64:ff9b::/96 NAT64 well-known（RFC 6052 §2.1）：4-11 全零，内嵌 IPv4
//     恰好落在末 4 字节。/32 内其余前缀长度（§2.2 的 /40 /48 /56 /64，含
//     RFC 8215 的 local-use /48）不在这里解码：内嵌位置随前缀长度变化、
//     IPv4 高低 16 位被 u 八位组隔开，按末 4 字节解出的不是内嵌地址——
//     这类前缀由 isBlockedIP 整体拒绝，不会走到这里。
//   - ::/96 已废弃的 IPv4-compatible（RFC 4291）：前 12 字节全零，内嵌
//     IPv4 在 12-15。::ffff: 映射段 10-11 是 ff，已被 To4() 收走，不会落到
//     这里。
func embeddedIPv4(ip net.IP) net.IP {
	ipv6 := ip.To16()
	if ipv6 == nil || ip.To4() != nil {
		return nil
	}
	// Teredo 客户端 IPv4 在 12-15 且按位取反。
	if ipv6[0] == 0x20 && ipv6[1] == 0x01 && ipv6[2] == 0 && ipv6[3] == 0 {
		out := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			out[i] = ^ipv6[12+i]
		}
		return out
	}
	// 6to4 内嵌 IPv4 在 2-5。
	if ipv6[0] == 0x20 && ipv6[1] == 0x02 {
		return net.IP(ipv6[2:6])
	}
	// NAT64 well-known 前缀与 IPv4-compatible 的内嵌 IPv4 都在末 4 字节。
	// /32 内非 /96 布局（含 local-use /48）的 bytes 4-11 不全零，不满足零
	// 校验；64:ff9a::/96（local-use /96）bytes 4-11 全零但前缀不匹配
	// isNAT64Prefix，也不在这里解码——它由 isBlockedIP 在调用之前整体拒绝，
	// 不会走到这里。两种形态都返回 nil。
	nat64 := isNAT64Prefix(ipv6) && allZero(ipv6[4:12])
	compatible := allZero(ipv6[:12])
	if nat64 || compatible {
		return net.IP(ipv6[12:16])
	}
	return nil
}

func BuildProxyURL(rawURL string) string {
	normalized, err := normalizeMediaURL(rawURL)
	if err != nil {
		return rawURL
	}
	return ProxyPath + "?url=" + url.QueryEscape(normalized)
}

func RewriteHTMLMediaSources(fragment string) string {
	if strings.TrimSpace(fragment) == "" {
		return fragment
	}

	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), root)
	if err != nil {
		return fragment
	}

	for _, node := range nodes {
		rewriteNodeMediaSources(node)
	}

	var buf bytes.Buffer
	for _, node := range nodes {
		if err := html.Render(&buf, node); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func rewriteNodeMediaSources(node *html.Node) {
	if node == nil {
		return
	}

	if node.Type == html.ElementNode {
		switch node.Data {
		case "img":
			rewriteAttr(node, "src")
		case "video":
			rewriteAttr(node, "src")
			rewriteAttr(node, "poster")
		case "source":
			rewriteAttr(node, "src")
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		rewriteNodeMediaSources(child)
	}
}

func rewriteAttr(node *html.Node, key string) {
	for i := range node.Attr {
		if strings.EqualFold(node.Attr[i].Key, key) {
			node.Attr[i].Val = BuildProxyURL(node.Attr[i].Val)
		}
	}
}

func RewriteMediaURL(rawURL string) string {
	return BuildProxyURL(rawURL)
}

func (s *ProxyService) GetCached(rawURL string) (*CachedMedia, bool, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, false, err
	}
	return s.getCachedNormalized(normalized, "")
}

// FetchAndCache 匿名拉取并缓存（公开附件）。私有仓库附件用
// FetchAndCacheWithBearer。
func (s *ProxyService) FetchAndCache(ctx context.Context, rawURL string) (*CachedMedia, error) {
	return s.fetchAndCache(ctx, rawURL, "", "")
}

// FetchAndCacheWithBearer 同 FetchAndCache，但请求带 Authorization: Bearer：
// 私有仓库的 user-attachments 需要 App 安装令牌才拉得到。bearer 只进请求头，
// 不进缓存；scope 是该令牌的授权域（调用方给安装 id 之类的不透明串），进
// 缓存键且记入 meta——不同域各是各的条目，匿名与其他 scope 一律按未命中
// 处理，防止私有内容经共享 URL 缓存跨授权域泄漏。
func (s *ProxyService) FetchAndCacheWithBearer(ctx context.Context, rawURL, bearer, scope string) (*CachedMedia, error) {
	return s.fetchAndCache(ctx, rawURL, bearer, scope)
}

func (s *ProxyService) fetchAndCache(ctx context.Context, rawURL, bearer, scope string) (*CachedMedia, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, err
	}

	lock := s.acquireDownloadLock(normalized)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		// 同 URL 已有下载在跑：等待必须吃 ctx。探测的 30s 时限是总时限，
		// 不能被同 URL 前一请求的 45s 客户端超时拖穿。
		return nil, ctx.Err()
	}
	defer func() { <-lock }()

	if cached, ok, err := s.getCachedNormalized(normalized, scope); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}

	if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
		return nil, markLocalCacheErr(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "fast-ship-media-proxy/1.0")
	// Bearer 令牌是仓库凭证，只对 GitHub 媒体域有意义；截图面放行任意公网
	// host，把令牌发给外部图床等于把 App 安装凭证送出去。
	if bearer != "" && IsGitHubMediaURL(normalized) {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &UpstreamStatusError{StatusCode: resp.StatusCode}
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !s.isAllowedContentType(contentType, normalized) {
		return nil, ErrUnsupportedUpstreamMedia
	}

	// 已知长度超限直接拒；未知长度（分块响应的 Content-Length 为 -1）不算
	// 超限，交给下面的 io.LimitReader + written 复核兜底。
	if contentLength := resp.ContentLength; contentLength > s.maxCacheEntrySize {
		return nil, ErrMediaTooLarge
	}

	tempFile, err := os.CreateTemp(s.cacheDir, defaultTempFilePattern)
	if err != nil {
		return nil, markLocalCacheErr(err)
	}

	downloadOK := false
	defer func() {
		tempFile.Close()
		if !downloadOK {
			_ = os.Remove(tempFile.Name())
		}
	}()

	// 写侧打标：写本地临时文件失败带 ErrLocalCache，读网络 body 失败仍是
	// 原始网络错误。
	written, err := io.Copy(cacheFileWriter{w: tempFile}, io.LimitReader(resp.Body, s.maxCacheEntrySize+1))
	if err != nil {
		return nil, err
	}
	// 200 + 0 字节（已知长度或分块空 body）不是可用的截图，落盘会变成一张
	// 「下载成功」的空缓存，直接拒。
	if written == 0 {
		return nil, ErrEmptyUpstreamMedia
	}
	if written > s.maxCacheEntrySize {
		return nil, ErrMediaTooLarge
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return nil, markLocalCacheErr(err)
	}

	now := time.Now().UTC()
	entry := cacheEntry{
		URL:          normalized,
		Scope:        scope,
		ContentType:  contentType,
		CacheControl: buildCacheControl(s.cacheTTL),
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
		Size:         written,
		CachedAt:     now,
		ExpiresAt:    now.Add(s.cacheTTL),
	}

	cachePath := s.dataPath(normalized, scope)
	metaPath := s.metaPath(normalized, scope)

	if err := os.Rename(tempFile.Name(), cachePath); err != nil {
		return nil, markLocalCacheErr(err) // *os.LinkError 也统一打标
	}
	metaBytes, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o640); err != nil {
		return nil, markLocalCacheErr(err)
	}

	downloadOK = true
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, nil
}

func (s *ProxyService) ProxyPass(ctx context.Context, rawURL, rangeHeader string, headOnly bool) (*PassThroughResponse, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, err
	}

	method := http.MethodGet
	if headOnly {
		method = http.MethodHead
	}

	req, err := http.NewRequestWithContext(ctx, method, normalized, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "fast-ship-media-proxy/1.0")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		return nil, &UpstreamStatusError{StatusCode: resp.StatusCode}
	}

	return &PassThroughResponse{
		StatusCode: resp.StatusCode,
		Header:     cloneRelevantHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

func (s *ProxyService) normalize(rawURL string) (string, error) {
	normalized, err := normalizeAllowedURL(rawURL, s.allowURL)
	if err != nil {
		return "", err
	}
	// 截图面只走 https：公网明文图片既不可信也不必要。
	if s.requireHTTPS {
		parsed, err := url.Parse(normalized)
		if err != nil || parsed.Scheme != "https" {
			return "", ErrInvalidMediaURL
		}
	}
	return normalized, nil
}

func (s *ProxyService) getCachedNormalized(normalized, scope string) (*CachedMedia, bool, error) {
	metaPath := s.metaPath(normalized, scope)
	cachePath := s.dataPath(normalized, scope)

	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		// 其余读失败（权限、目录损坏）降级：best-effort 清掉这一条，清得掉按
		// miss 走重下，清不掉才上报（见 discardCacheEntry）。
		return s.discardCacheEntry(metaPath, cachePath)
	}

	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(cachePath)
		return nil, false, nil
	}

	// 授权域不匹配的兜底检查：scope 已进缓存键，正常不会同键撞域；meta 里
	// 的 Scope 字段是双保险（如旧格式条目或键派生变化时仍不误命中）。
	if entry.Scope != scope {
		return nil, false, nil
	}

	if entry.expired(time.Now().UTC()) {
		_ = os.Remove(metaPath)
		_ = os.Remove(cachePath)
		return nil, false, nil
	}

	info, err := os.Stat(cachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(metaPath)
			return nil, false, nil
		}
		// meta 在而 data 读不到（权限、损坏）：同样降级重下，不把一次可自愈
		// 的单条损坏上报成目录级故障。
		return s.discardCacheEntry(metaPath, cachePath)
	}

	entry.Size = info.Size()
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, true, nil
}

// discardCacheEntry 单条缓存不可读时的降级清理：best-effort 删除 meta 与 data，
// 都清得掉（或本就不在）按 miss 返回，让本次请求走重下；清理失败说明是目录级
// 故障（权限/磁盘），才以 ErrLocalCache 上报——降级不了的现场必须留错误，否
// 则每次请求都静默重复同一失败且无法自愈。
func (s *ProxyService) discardCacheEntry(metaPath, cachePath string) (*CachedMedia, bool, error) {
	for _, err := range []error{os.Remove(metaPath), os.Remove(cachePath)} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, markLocalCacheErr(err)
		}
	}
	return nil, false, nil
}

// acquireDownloadLock 返回该 URL 的下载闸（容量 1 信号量）：发送成功即持有，
// 下载完成后 <-lock 收回。条目创建在 s.mu 下做，是否等待、等多久由调用方
// 结合 ctx 决定。
func (s *ProxyService) acquireDownloadLock(key string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock, ok := s.download[key]
	if !ok {
		lock = make(chan struct{}, 1)
		s.download[key] = lock
	}
	return lock
}

func (s *ProxyService) dataPath(normalized, scope string) string {
	return filepath.Join(s.cacheDir, cacheKey(scopedCacheID(normalized, scope))+".bin")
}

func (s *ProxyService) metaPath(normalized, scope string) string {
	return filepath.Join(s.cacheDir, cacheKey(scopedCacheID(normalized, scope))+".json")
}

// scopedCacheID 缓存键底材：令牌拉取的私有内容按授权域分键，不同安装/匿名
// 各是各的条目，不会互相命中或覆写；匿名（scope=""）保持纯 URL 键，存量缓
// 存不受影响。
func scopedCacheID(normalized, scope string) string {
	if scope == "" {
		return normalized
	}
	return scope + "\x1f" + normalized
}

func cloneRelevantHeaders(header http.Header) http.Header {
	out := make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control", "ETag", "Last-Modified"} {
		for _, value := range header.Values(key) {
			out.Add(key, value)
		}
	}
	if out.Get("Cache-Control") == "" {
		out.Set("Cache-Control", buildCacheControl(defaultCacheTTL))
	}
	return out
}

func normalizeAllowedURL(rawURL string, allowURL func(*url.URL) bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", ErrInvalidMediaURL
	}
	if parsed == nil || parsed.Host == "" || parsed.Scheme == "" {
		return "", ErrInvalidMediaURL
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", ErrInvalidMediaURL
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", ErrInvalidMediaURL
	}
	if !allowURL(parsed) {
		return "", ErrInvalidMediaURL
	}
	return parsed.String(), nil
}

func normalizeMediaURL(rawURL string) (string, error) {
	return normalizeAllowedURL(rawURL, isAllowedGitHubMediaURL)
}

// IsGitHubMediaURL URL 是否属于 GitHub 媒体域（与 media-proxy 白名单同一
// 口径，且必须是完整的 https URL：有 host、无 userinfo、无 fragment）。
// service 层用它决定下载失败后是否值得带 App 安装令牌重试——外部图床拿了
// 令牌等于拿到仓库凭证，所以按「可发请求的地址」准入而不是只看域名；http
// 明文的 GitHub 域名同样不算（截图下载面只走 https）。只影响这个导出闸门，
// isAllowedGitHubMediaURL 保持原语义（GitHub 媒体面有意允许 http）。
func IsGitHubMediaURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	return isAllowedGitHubMediaURL(parsed)
}

// IsGitHubAttachmentURL URL 是否是 GitHub 附件形态的直链：github.com 的
// user-attachments/assets 附件，或 user-images.githubusercontent.com 用户
// 图片——GitHub 拖入/粘贴附件的标准形态，路径通常没有文件名扩展名。service
// 层收集 PR 描述裸贴链接时用它豁免「必须有位图扩展名」的要求；能否发 App
// 安装令牌仍由 IsGitHubMediaURL 按「可发请求的地址」把关，两个口径独立。
// 按 URL 解析出 host 再判，前缀嵌在路径里的仿冒地址（evil.com/github.com/…）
// 不算。
func IsGitHubAttachmentURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "github.com" {
		return strings.HasPrefix(parsed.EscapedPath(), "/user-attachments/assets/")
	}
	return host == "user-images.githubusercontent.com"
}

func isAllowedGitHubMediaURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "github.com":
		return strings.HasPrefix(parsed.EscapedPath(), "/user-attachments/assets/")
	case host == "githubusercontent.com":
		return true
	case host == "github.githubassets.com":
		return true
	case host == "githubassets.com":
		return true
	case strings.HasPrefix(host, "github-production-user-asset-") && strings.HasSuffix(host, ".s3.amazonaws.com"):
		return true
	case strings.HasSuffix(host, ".githubusercontent.com"):
		return true
	case strings.HasSuffix(host, ".githubassets.com"):
		return true
	default:
		return false
	}
}

// isAllowedContentType 内容类型准入：GitHub 媒体面收图片与视频；截图面
// （imagesOnly）只收位图——octet-stream 时按扩展名回退，svg 即使标着
// image/* 也拒（视觉审查只吃位图）。
func (s *ProxyService) isAllowedContentType(contentType string, rawURL string) bool {
	if contentType == "" {
		if s.imagesOnly {
			return hasBitmapExtension(rawURL)
		}
		return hasGitHubMediaExtension(rawURL)
	}

	baseType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if s.imagesOnly {
		// svg 即使标着 image/* 也不收：image/svg+xml 是标准写法，image/svg
		// （不带 +xml）是上游会用到的非标准变体，一并按前缀拒。
		if strings.HasPrefix(baseType, "image/svg") {
			return false
		}
		return strings.HasPrefix(baseType, "image/") ||
			(baseType == "application/octet-stream" && hasBitmapExtension(rawURL))
	}
	if strings.HasPrefix(baseType, "image/") || strings.HasPrefix(baseType, "video/") {
		return true
	}
	if baseType == "application/octet-stream" {
		return hasGitHubMediaExtension(rawURL)
	}
	return false
}

var (
	// bitmapExtensions 视觉审查能吃的位图。svg 是矢量图，不在此列——截图
	// 面既不按扩展名收它，也不收 image/svg+xml。
	bitmapExtensions = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".bmp": true,
	}
	// vectorExtensions GitHub 媒体面额外收 svg。
	vectorExtensions = map[string]bool{".svg": true}
	videoExtensions  = map[string]bool{
		".mp4": true, ".mov": true, ".webm": true, ".m4v": true,
	}
)

// hasGitHubMediaExtension GitHub 媒体面的扩展名集合 = 位图 + svg + 视频。
func hasGitHubMediaExtension(rawURL string) bool {
	ext := urlPathExtension(rawURL)
	return bitmapExtensions[ext] || vectorExtensions[ext] || videoExtensions[ext]
}

func hasBitmapExtension(rawURL string) bool {
	return bitmapExtensions[urlPathExtension(rawURL)]
}

// HasBitmapExtension URL 路径是否以位图扩展名（.png .jpg .jpeg .gif .webp
// .bmp）结尾。导出给 service 层做 PR 描述裸链接收集的过滤——收集口径与下载
// 面 imagesOnly 的扩展名集合必须同源，两边各写一份迟早漂移。
func HasBitmapExtension(rawURL string) bool {
	return hasBitmapExtension(rawURL)
}

func urlPathExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(parsed.Path))
}

func buildCacheControl(ttl time.Duration) string {
	seconds := int(ttl / time.Second)
	if seconds <= 0 {
		seconds = int(defaultCacheTTL / time.Second)
	}
	return "public, max-age=" + strconv.Itoa(seconds)
}

func cacheKey(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

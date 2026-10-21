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
)

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
	return &ProxyService{
		client:            cfg.client,
		cacheDir:          filepath.Join(cacheRoot, cfg.cacheSubdir),
		cacheTTL:          cfg.cacheTTL,
		maxCacheEntrySize: cfg.maxCacheEntrySize,
		allowURL:          cfg.allowURL,
		requireHTTPS:      cfg.requireHTTPS,
		imagesOnly:        cfg.imagesOnly,
		download:          make(map[string]chan struct{}),
	}
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
// 多播/未指定地址，以及 169.254.0.0/16、100.64.0.0/10（CGNAT）、0.0.0.0/8、
// 198.18.0.0/15（基准测试）、240.0.0.0/4（保留段，含广播）地址；IPv6 还要
// 解出内嵌 IPv4 再判一遍（NAT64 只有 well-known /96 解码，/32 内其余前缀
// 长度整体拒绝，见下）；公网地址放行。
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
		case ip4[0] == 198 && ip4[1] >= 18 && ip4[1] <= 19: // 198.18.0.0/15 基准测试
			return true
		case ip4[0] >= 240: // 240.0.0.0/4 保留（含广播 255.255.255.255）
			return true
		}
		return false
	}
	// NAT64/6to4/Teredo 等前缀本身是公网单播，能内嵌任意 IPv4——载荷是
	// 内网地址时同样要拒，否则「看起来是公网」的 IPv6 就绕过了上面的判定。
	// NAT64（64:ff9b::/32，RFC 6052/8215）统一成一条规则：只有 well-known
	// /96（bytes 4-11 全零）按末 4 字节解出内嵌 IPv4 递归判定（内嵌公网
	// 放行）；/32 内其余前缀长度——RFC 6052 §2.2 的 /40 /48 /56 /64 布局
	// （IPv4 高低 16 位被 u 八位组隔开、位置随前缀长度变化，按任何固定偏移
	// 都解不出可靠的内嵌 IPv4）与 RFC 8215 的 local-use /48（布局还可由
	// 网络自行选择）——一律整体拒绝。这些前缀不该出现在公网图床上，宁可
	// 错拒不可放过，有意保守。
	if ipv6 := ip.To16(); ipv6 != nil && isNAT64Prefix(ipv6) {
		if allZero(ipv6[4:12]) {
			if v4 := embeddedIPv4(ip); v4 != nil && isBlockedIP(v4) {
				return true
			}
			return false
		}
		return true
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
	// 校验，到这里返回 nil，由 isBlockedIP 按整体拒绝处理。
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
	return s.getCachedNormalized(normalized)
}

// FetchAndCache 匿名拉取并缓存（公开附件）。私有仓库附件用
// FetchAndCacheWithBearer。
func (s *ProxyService) FetchAndCache(ctx context.Context, rawURL string) (*CachedMedia, error) {
	return s.fetchAndCache(ctx, rawURL, "")
}

// FetchAndCacheWithBearer 同 FetchAndCache，但请求带 Authorization: Bearer：
// 私有仓库的 user-attachments 需要 App 安装令牌才拉得到。bearer 只进请求头，
// 不进缓存键，与匿名路径共用同一份缓存。
func (s *ProxyService) FetchAndCacheWithBearer(ctx context.Context, rawURL, bearer string) (*CachedMedia, error) {
	return s.fetchAndCache(ctx, rawURL, bearer)
}

func (s *ProxyService) fetchAndCache(ctx context.Context, rawURL, bearer string) (*CachedMedia, error) {
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

	if cached, ok, err := s.getCachedNormalized(normalized); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}

	if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
		return nil, err
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
		return nil, err
	}

	downloadOK := false
	defer func() {
		tempFile.Close()
		if !downloadOK {
			_ = os.Remove(tempFile.Name())
		}
	}()

	written, err := io.Copy(tempFile, io.LimitReader(resp.Body, s.maxCacheEntrySize+1))
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
		return nil, err
	}

	now := time.Now().UTC()
	entry := cacheEntry{
		URL:          normalized,
		ContentType:  contentType,
		CacheControl: buildCacheControl(s.cacheTTL),
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
		Size:         written,
		CachedAt:     now,
		ExpiresAt:    now.Add(s.cacheTTL),
	}

	cachePath := s.dataPath(normalized)
	metaPath := s.metaPath(normalized)

	if err := os.Rename(tempFile.Name(), cachePath); err != nil {
		return nil, err
	}
	metaBytes, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o640); err != nil {
		return nil, err
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

func (s *ProxyService) getCachedNormalized(normalized string) (*CachedMedia, bool, error) {
	metaPath := s.metaPath(normalized)
	cachePath := s.dataPath(normalized)

	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(cachePath)
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
		return nil, false, err
	}

	entry.Size = info.Size()
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, true, nil
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

func (s *ProxyService) dataPath(normalized string) string {
	return filepath.Join(s.cacheDir, cacheKey(normalized)+".bin")
}

func (s *ProxyService) metaPath(normalized string) string {
	return filepath.Join(s.cacheDir, cacheKey(normalized)+".json")
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
// 口径）。service 层用它决定下载失败后是否值得带 App 安装令牌重试——外部
// 图床拿了令牌等于拿到仓库凭证。
func IsGitHubMediaURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return isAllowedGitHubMediaURL(parsed)
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
		if baseType == "image/svg+xml" {
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

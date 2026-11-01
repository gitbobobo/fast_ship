// Package githubmedia 的缓存面边界（不变量，改动前先对照）：
//
//  1. 面只有三个，规则按面定，不按调用点定：
//     - 媒体代理面（defaultCacheSubdir）：缓存可丢弃。TTL 回收、scope 标记
//     缺失时整目录清空、损坏条目回收，只发生在这里。
//     - 审查截图面（review-screenshot-cache）：缓存不可丢弃——.bin 是
//     review_assets 持久资产（storage_path 直指文件、ensureAssets 不重下）。
//     任何代码路径不得删除它的条目；启动只建目录+补标记；无 TTL 删除、
//     无 purge、无条目回收 sweeper（media-*.tmp 清理是唯一例外，
//     sweepStaleTmpFiles 不触碰 .bin/.json）；读路径命中即返回（不过期、
//     不因类型复核 miss）。
//     - 无缓存出口（issue assets、avatars、screenshot content、crop）：不落
//     缓存目录，只施加同源硬化头与「attachment 或 inline 图片档」判据（按
//     出口语义：下载类 attachment，截图/裁剪类刻意保留 inline）。
//  2. MIME/扩展门：媒体代理面全量（image/svg*、.svg、HTML 拒 + fallback 归
//     一化）；审查面只收位图；下载出口 octet-stream+attachment 不进文档上下
//     文。
//  3. Cache-Control 一律 private（URL 带 JWT），TTL 由 s.cacheTTL 派生，
//     cloneRelevantHeaders 与缓存命中同源，不透传上游。
//  4. 写侧两步发布（data→binPath 再 meta→metaPath）。回收者对已 rename 走
//     的文件，恢复前必须重读校验新鲜度，且恢复前校验目标路径未被写侧占据；
//     不能安全恢复就丢弃旧字节，绝不覆盖新条目。
//  5. 版本参数 v：前端无条件重写为当前值；Go ProxyURLVersion 与前端
//     MEDIA_PROXY_VERSION 由 TestProxyURLVersionMatchesFrontend 断言一致，
//     只涨一边会直接红。
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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
)

const ProxyPath = "/api/github/media-proxy"

// ProxyURLVersion 代理地址的缓存失效版本，随媒体响应的安全语义（准入、硬化
// 头）递增。浏览器缓存的是整条响应：旧版本 URL 上缓存过的 svg 在部署后仍会
// 命中本地缓存、根本不回源，闸门与硬化头对它们无效。带版本参数的 URL 与旧
// 缓存键不同，一次版本跳跃就能让所有前置缓存失效。前端
// `web/src/lib/utils/github-media-proxy.ts` 里的同名常量必须跟着改。
const ProxyURLVersion = "2"

// ProxyURLVersionParam 版本参数名。
const ProxyURLVersionParam = "v"

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
	// ErrUnsupportedSVG 命中 svg 的固定拒绝，分两档：内容类型是 image/svg*
	//（按响应类型判定，同一资源下次还是 svg，出口可按较长 TTL 缓存）；上游
	// 没给类型而扩展名是 .svg（ErrUnsupportedSVGByExtension，判据依赖上游本
	// 次响应——下次它可能给正常类型，只能按很短 TTL）。都包装
	// ErrUnsupportedUpstreamMedia，下游分类不变；单独的哨兵是为了让出口区分
	// 「接近确定的拒绝」（可缓存）与「上游这一回回的不是媒体」（可能是拦截
	// 页/登录页，钉住会让图片持续裂图，不能缓存）。
	ErrUnsupportedSVG            = fmt.Errorf("%w: svg", ErrUnsupportedUpstreamMedia)
	ErrUnsupportedSVGByExtension = fmt.Errorf("%w (by extension)", ErrUnsupportedSVG)
	ErrBlockedUpstreamAddress    = errors.New("图片地址指向内网或保留地址")
	ErrEmptyUpstreamMedia        = errors.New("上游返回空内容")
	ErrMediaTooLarge             = errors.New("图片超过大小上限")
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
	client   *http.Client
	cacheDir string
	// cacheSubdir 该服务实例的缓存面子目录；读路径按它区分媒体代理面（TTL
	// 与退役生效）和 review 截图面（持久资产，命中即返回，不过期不复核）。
	cacheSubdir       string
	cacheTTL          time.Duration
	maxCacheEntrySize int64
	allowURL          func(*url.URL) bool
	// requireHTTPS 只接受 https 上游（截图面）；imagesOnly 只收图片内容。
	// GitHub 媒体面两者全关：白名单语义 + 图片视频都收，与截图面是两套准入。
	requireHTTPS bool
	imagesOnly   bool
	// githubWebHost 部署的 GitHub Web 地址 host[:port]（见 GitHubWebHost）：只
	// 用于「能否把 App 安装令牌发给该上游」的判定；空值按公共 GitHub 处理。
	githubWebHost string

	mu sync.Mutex
	// download 每 URL 一把容量 1 的信号量（发送即持有、接收即释放）：同 URL
	// 下载串行，但等待必须吃 ctx——sync.Mutex 锁不来取消，30s 探测时限会被
	// 同 URL 前一请求的 45s 客户端超时拖穿。refs 是该闸的引用计数（持有者 +
	// 等待者，acquire 加、release/abandon 减）：空闲回收的判据是计数归零而
	// 不是 len(chan)——等待者拿到 channel 后在 s.mu 之外做发送，那个窗口里
	// channel 长度看不到等待者，凭长度删除会把等待者的闸变成孤儿，同一 URL
	// 出现两个并发下载。
	download map[string]*downloadGate

	// metaReadFailMu/At/metaReadFailLogger 缓存 meta 读失败的限频告警：目录
	// 级故障（权限被改、EIO、只读挂载）会退化成每个请求静默重下，不打日志
	// 只能靠「图片变慢」感知；同类告警间隔内只记一次。
	metaReadFailMu     sync.Mutex
	metaReadFailAt     map[string]time.Time
	metaReadFailLogger *slog.Logger
}

// metaReadFailLogInterval 同一 cacheDir 的 meta 读失败告警最小间隔。
const metaReadFailLogInterval = 10 * time.Minute

// NewProxyServiceWithLogger 带日志的生产装配：meta 读失败的目录级故障按限
// 频 warn 记录（读路径不删文件的约束不变，日志是可观测性，不是行为变化）。
func NewProxyServiceWithLogger(cacheRoot string, logger *slog.Logger) *ProxyService {
	s := NewProxyService(cacheRoot)
	s.metaReadFailLogger = logger
	s.metaReadFailAt = make(map[string]time.Time)
	return s
}

// logMetaReadFail 限频记录 meta 读失败：同一 cacheDir 在间隔内只记一次。
func (s *ProxyService) logMetaReadFail(metaPath string, err error) {
	if s.metaReadFailLogger == nil {
		return
	}
	s.metaReadFailMu.Lock()
	defer s.metaReadFailMu.Unlock()
	if s.metaReadFailAt == nil {
		s.metaReadFailAt = make(map[string]time.Time)
	}
	if last, ok := s.metaReadFailAt[s.cacheDir]; ok && time.Since(last) < metaReadFailLogInterval {
		return
	}
	s.metaReadFailAt[s.cacheDir] = time.Now()
	s.metaReadFailLogger.Warn("缓存 meta 读取失败，按 miss 重下（目录级故障时每条请求都会重下）",
		"path", metaPath, "error", err)
}

// downloadGate 同 URL 下载闸：容量 1 的信号量加引用计数，计数由持有者在
// s.mu 下增减（见 acquire/release/abandonDownloadLock）。
type downloadGate struct {
	sem  chan struct{}
	refs int
}

// serviceConfig newService 的装配参数；零值字段按 GitHub 媒体代理语义兜底。
type serviceConfig struct {
	cacheSubdir       string
	cacheTTL          time.Duration
	maxCacheEntrySize int64
	client            *http.Client
	// transport 覆盖默认客户端的 Transport（测试注入）；nil 用原构造。
	transport    http.RoundTripper
	allowURL     func(*url.URL) bool
	requireHTTPS bool
	imagesOnly   bool
	guardAddress bool
	// trustedPrivateHosts 可信内网主机名单（仅 guardAddress 时有意义）：
	// 拨号时的主机名与名单完全一致（不区分大小写、不含端口、不支持
	// 通配符）即跳过解析 IP 的内网检查；名单外主机与名单内主机重定向
	// 出去的建连照旧检查。
	trustedPrivateHosts []string
	// trustedPrivateHostsFunc 动态可信内网主机清单：每次拨号现取（新配的
	// 集成主机不用重启即生效）。与 trustedPrivateHosts 取并集——面向
	// 「allow_private_network 的 Forgejo 集成主机」这类运行时来源。
	trustedPrivateHostsFunc func() []string
	// githubWebHost 部署的 GitHub Web 地址 host[:port]（同
	// ProxyService.githubWebHost）：凭证闸门据此放行部署实例自己的附件路径，
	// 空值按公共 GitHub 处理。
	githubWebHost string
}

// ProxyOption NewProxyService 的可选装配项；生产装配不传即为默认 GitHub
// 白名单语义。handler 层直测用它注入指向 httptest 假上游的客户端，测试
// 不打真实网络。
type ProxyOption func(*serviceConfig)

// WithHTTPTransport 只替换默认客户端的 Transport（测试注入指向 httptest 假
// 上游的 RoundTripper，不打真实网络），客户端的 CheckRedirect（白名单复检、
// Authorization 剥离）与超时保持生产实现。
//
// 边界：截图面（guardAddress）的 SSRF 防线就长在 Transport 里（拨号期 IP 校
// 验），整颗换 Transport 会把那层静默拆掉——所以该选项撞上 guardAddress 面
// 直接 panic（见 newService），不要拿它去接截图面。
func WithHTTPTransport(rt http.RoundTripper) ProxyOption {
	return func(cfg *serviceConfig) { cfg.transport = rt }
}

// NewProxyService GitHub 媒体代理面：GitHub 域名白名单、图片视频都收，
// 服务 /api/github/media-proxy。
func NewProxyService(cacheRoot string, opts ...ProxyOption) *ProxyService {
	cfg := serviceConfig{
		cacheSubdir: defaultCacheSubdir,
		allowURL:    isAllowedGitHubMediaURL,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return newService(cacheRoot, cfg)
}

// NewReviewMediaService PR 审查截图下载面：放行任意公网 https 图片，SSRF
// 防线不在域名白名单，而在拨号时校验解析出的 IP——重定向与 DNS 重绑定
// 的每次建连都会被拦。与 NewProxyService 是两个独立实例，别混用。
// githubWebHost 是部署的 GitHub Web 地址 host[:port]（GitHubWebHost 从
// github_app.api_base_url 推导）：私有仓库附件的 App 安装令牌只发给它（见
// IsGitHubMediaURLFor），GHE 部署的附件才不会卡在凭证闸门上。
// trustedPrivateHosts 是部署配置的可信内网主机名单：拨号主机名与名单完全
// 一致（不区分大小写、不含端口、不支持通配符）时跳过内网 IP 检查——内网
// DNS 把自建服务（如 UIV）直接解析到内网地址的部署靠它放行，空名单与
// 旧行为一致。
// trustedPrivateHostsFunc 是可选的动态可信主机来源（如允许私网的 Forgejo
// 集成主机清单）——每次拨号现取，与静态名单取并集，传 nil 与旧行为一致。
func NewReviewMediaService(cacheRoot, githubWebHost string, trustedPrivateHosts []string, trustedPrivateHostsFunc func() []string) *ProxyService {
	return newService(cacheRoot, serviceConfig{
		cacheSubdir:             "review-screenshot-cache",
		allowURL:                allowPublicImageHost,
		requireHTTPS:            true,
		imagesOnly:              true,
		guardAddress:            true,
		trustedPrivateHosts:     trustedPrivateHosts,
		trustedPrivateHostsFunc: trustedPrivateHostsFunc,
		githubWebHost:           githubWebHost,
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
		webHost := cfg.githubWebHost
		cfg.client = newGuardedClient(cfg.requireHTTPS, cfg.allowURL, cfg.guardAddress,
			cfg.trustedPrivateHosts, cfg.trustedPrivateHostsFunc,
			func(u *url.URL) bool { return IsGitHubMediaURLFor(u.String(), webHost) })
	}
	if cfg.transport != nil {
		if cfg.guardAddress {
			// 截图面的 SSRF 防线是 Transport 里的 Dialer.Control 拨号校验，整颗
			// 换 Transport 就会静默失去它。宁可炸：测试注入只该用在媒体面。
			panic("githubmedia: WithHTTPTransport 不能用于 guardAddress 面（会移除拨号期 IP 校验）")
		}
		// 克隆后只换 Transport：CheckRedirect/Timeout 是重定向白名单与凭证
		// 剥离的执行点，不能被测试注入旁路。
		clone := *cfg.client
		clone.Transport = cfg.transport
		cfg.client = &clone
	}
	s := &ProxyService{
		client:            cfg.client,
		cacheDir:          filepath.Join(cacheRoot, cfg.cacheSubdir),
		cacheSubdir:       cfg.cacheSubdir,
		cacheTTL:          cfg.cacheTTL,
		maxCacheEntrySize: cfg.maxCacheEntrySize,
		allowURL:          cfg.allowURL,
		requireHTTPS:      cfg.requireHTTPS,
		imagesOnly:        cfg.imagesOnly,
		githubWebHost:     cfg.githubWebHost,
		download:          make(map[string]*downloadGate),
	}
	// purge 只允许媒体代理面跑（按 cacheSubdir 判定）：review 截图面的目录
	// 是 review_assets 的持久存储（无副本、ensure 不会重下），标记缺失时清
	// 空等于删光历史审查截图。
	s.purgeLegacyCacheEntries(cfg.cacheSubdir == defaultCacheSubdir)
	return s
}

// StartExpiredSweeper 启动后台目录过期扫描（启动即扫一遍 + 每
// expiredSweepInterval 一次）。由生产装配显式调用——构造函数里无条件起后台
// 协程会让测试的 seed 与扫描互相踩（扫描可能落在写 seed 的中间）。测试直接
// 调 sweepExpiredEntries（导出为 SweepExpiredEntries）。
func (s *ProxyService) StartExpiredSweeper() {
	s.startExpiredSweeper()
}

// StartStaleTmpSweeper 启动后台 media-*.tmp 清理（随过期扫描一起跑）。给不
// 接过期回收的面用（review 截图面：.bin/.json 是持久资产不回收，但中断下载
// 的 tmp 残留同样要清）。
func (s *ProxyService) StartStaleTmpSweeper() {
	go func() {
		s.sweepStaleTmpFiles(time.Now().UTC())
		for {
			time.Sleep(expiredSweepInterval)
			s.sweepStaleTmpFiles(time.Now().UTC())
		}
	}()
}

// SweepExpiredEntries 手动触发一次目录过期扫描（测试与运维用）。
func (s *ProxyService) SweepExpiredEntries() {
	s.sweepExpiredEntries()
}

// expiredSweepInterval 目录过期扫描的间隔。读路径不再删文件（见
// getCachedNormalized），过期条目由这里低频回收：每次扫描删掉已过期的
// meta/data；「不再被成功重下」的条目（上游已删、永久 415）也由此退役，
// 缓存目录不会只增不减。
const expiredSweepInterval = 6 * time.Hour

// startExpiredSweeper 启动即扫一遍，之后按固定间隔后台扫。失败静默：扫描是
// 保洁，不构成任何请求路径的前置条件。
func (s *ProxyService) startExpiredSweeper() {
	go func() {
		s.sweepExpiredEntries()
		for {
			time.Sleep(expiredSweepInterval)
			s.sweepExpiredEntries()
		}
	}()
}

// sweepExpiredEntries 扫一遍缓存目录，删掉已过期的条目。meta 是唯一事实源
// （data 由 meta 的存在性背书），只读 meta 判断；meta 读不了/解析不了的孤儿
// 文件留待下次覆盖，不猜。
//
// 与写盘的竞争处理：meta 落后于 data（写侧先 rename data 再 WriteFile meta），
// 只靠「重读 meta 比对 CachedAt」堵不住「读到旧 meta、写侧已 rename 新 data、
// 尚未写新 meta」的窗口。扫描删除前持该 key 的下载闸（与 fetchAndCache 同
// 一把锁），互斥后旧过期条目在窗口内也只可能被覆盖成新条目——锁内重读发现
// 不再过期就跳过；先删 data 再删 meta，第二步失败或进程被杀只会留下孤儿
// .bin（没有 meta 它不可达），孤儿按 mtime 超过 2×TTL 纳入清理，不会永久堆
// 积。meta 里存了原始 URL（缓存键底材），足以还原下载闸的 key。
func (s *ProxyService) sweepExpiredEntries() {
	// 面边界（不变量第 1 条）：条目回收只存在于媒体代理面。护栏做在函数入
	// 口而不是 main.go 装配——StartExpiredSweeper/SweepExpiredEntries 对所
	// 有实例可见，误接或手动触发 review 实例就会把持久审查截图删成永久
	// 404（storage_path 直指文件、ensureAssets 不重下）。
	if s.cacheSubdir != defaultCacheSubdir {
		return
	}
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	// 扫描顺带回收上次进程被杀残留在隔离名的 .gone（.json.gone 与 .bin.gone
	// 都是 rename 后 remove 前被杀的残留，无人可达）。阈值与隔离判据一致用
	// 2×TTL：goneBin 的 mtime 继承原 data，落在 1×~2×TTL 区间的隔离文件若是
	// 写侧刚发布的，按 1×TTL 会被另一轮扫描误回收。
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gone") {
			info, err := e.Info()
			if err == nil && now.Sub(info.ModTime()) > 2*s.cacheTTL {
				_ = os.Remove(filepath.Join(s.cacheDir, e.Name()))
			}
		}
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		metaPath := filepath.Join(s.cacheDir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var entry cacheEntry
		if json.Unmarshal(data, &entry) != nil {
			// 损坏 meta（写盘中断在 rename data 与 WriteFile meta 之间、或磁
			// 盘满截断）：与孤儿 .bin 同口径按 mtime 超 2×TTL 回收（meta 与同
			// 名 .bin 一起删），否则该条目连同最大 256MB 的 .bin 永久留盘且每
			// 次命中都重源。mtime 新的是写侧中间态，不碰。两处删除（.bin 存
			// 在/缺失）都先 rename 成隔离名再删，压掉 stat→remove 窗口。
			//
			// 同一 URL 可能正在因「损坏 meta 读路径判 miss」而重下，删除与过
			// 期分支一样持该 key 的下载闸（锁内重读复核 mtime 仍超龄，被写侧
			// 覆盖成好 meta 就放弃），不无锁动手。URL 不可得（0 字节/开头即截
			// 断的 meta 解析不出 url 字段，闸键会是空串、与写侧不同键无互斥）
			// 时不持闸硬删——退回孤儿判据：同名 .bin 的 mtime 也超 2×TTL 才
			// 删（写侧 rename 的新 data mtime 必是新，不会误删刚发布的条目）。
			binPath := strings.TrimSuffix(metaPath, ".json") + ".bin"
			if entry.URL == "" {
				// 闸键不可得，与写侧没有互斥，靠「只删旧东西」压窗口：.bin 存
				// 在时看它的 mtime（写侧刚 rename 的新 data mtime 必是新）；
				// .bin 不存在时看 meta 自身的 mtime——没有 data 可误删，meta
				// 超龄就该回收（旧版本这条路径会永久留盘）。stat 与 remove 之
				// 间的窗口用「先 rename 成隔离名再删」压掉：写侧若正要发布，
				// 它的 rename(data) 与 WriteFile(meta) 都不会命中已被改名的路
				// 径，本次删的确定是隔离出来的旧文件。
				binInfo, statErr := os.Stat(binPath)
				switch {
				case statErr == nil:
					if now.Sub(binInfo.ModTime()) < 2*s.cacheTTL {
						continue
					}
				case !errors.Is(statErr, os.ErrNotExist):
					continue
				default:
					if now.Sub(info.ModTime()) < 2*s.cacheTTL {
						continue
					}
					gonePath := metaPath + ".gone"
					if err := os.Rename(metaPath, gonePath); err != nil {
						continue
					}
					// rename 前的复核没在锁内，期间写侧可能已把同一 meta 覆盖成
					// 好条目：再读一次 .gone，能解析成好 meta 就还回去。
					if data, err := os.ReadFile(gonePath); err == nil {
						var entry cacheEntry
						if json.Unmarshal(data, &entry) == nil && !entry.expired(now) {
							_ = os.Rename(gonePath, metaPath)
							continue
						}
					}
					_ = os.Remove(gonePath)
					continue
				}
				// 与缺失分支同口径：rename 成隔离名再删，stat→remove 窗口内
				// 写侧刚发布的文件不会被误命中。
				goneBin := binPath + ".gone"
				goneMeta := metaPath + ".gone"
				if err := os.Rename(binPath, goneBin); err != nil {
					continue
				}
				// rename 后看一眼 mtime：写侧恰好在这个窗口 rename 了新 data
				// 的话，goneBin 的时间是新鲜的——原子放回，本轮跳过。
				if goneInfo, err := os.Stat(goneBin); err == nil && now.Sub(goneInfo.ModTime()) < 2*s.cacheTTL {
					restoreGoneBin(goneBin, binPath)
					continue
				}
				if err := os.Rename(metaPath, goneMeta); err != nil {
					restoreGoneBin(goneBin, binPath)
					continue
				}
				// goneMeta 是两步发布的第二步：写侧可能恰在这个窗口写完新
				// meta——能解析成未过期条目说明新条目已发布。此时只还 meta，
				// 旧字节交给 restoreGoneBin 原子放回（写侧已占 binPath 就自动
				// 丢弃，绝不覆盖新条目）。
				if data, err := os.ReadFile(goneMeta); err == nil {
					var recheckEntry cacheEntry
					if json.Unmarshal(data, &recheckEntry) == nil && !recheckEntry.expired(now) {
						_ = os.Rename(goneMeta, metaPath)
						restoreGoneBin(goneBin, binPath)
						continue
					}
				}
				_ = os.Remove(goneMeta)
				restoreGoneBin(goneBin, binPath)
				continue
			}
			if now.Sub(info.ModTime()) < 2*s.cacheTTL {
				continue
			}
			gate := s.acquireDownloadLock(entry.URL)
			select {
			case gate.sem <- struct{}{}:
			case <-time.After(5 * time.Second):
				s.abandonDownloadLock(entry.URL, gate)
				continue
			}
			recheck, err := os.ReadFile(metaPath)
			if err != nil {
				s.releaseDownloadLock(entry.URL, gate)
				continue
			}
			var recheckEntry cacheEntry
			if json.Unmarshal(recheck, &recheckEntry) == nil {
				// 被写侧覆盖成了好 meta（或换了个内容），放弃。
				s.releaseDownloadLock(entry.URL, gate)
				continue
			}
			recheckInfo, err := os.Stat(metaPath)
			if err != nil || now.Sub(recheckInfo.ModTime()) < 2*s.cacheTTL {
				s.releaseDownloadLock(entry.URL, gate)
				continue
			}
			_ = os.Remove(filepath.Join(s.cacheDir, strings.TrimSuffix(name, ".json")+".bin"))
			_ = os.Remove(metaPath)
			s.releaseDownloadLock(entry.URL, gate)
			continue
		}
		if !entry.expired(now) {
			continue
		}
		// 持该 key 的下载闸再复核与删除：与同 key 写盘互斥，窗口关闭。闸等待
		// 吃 ctx（背景扫描用 Background），锁被长下载持有时本轮跳过即可，下
		// 轮再扫。
		gate := s.acquireDownloadLock(entry.URL)
		select {
		case gate.sem <- struct{}{}:
		case <-time.After(5 * time.Second):
			s.abandonDownloadLock(entry.URL, gate)
			continue
		}
		// 锁内重读：条目可能刚被覆盖成新条目（不再过期）或已被清走。
		recheck, err := os.ReadFile(metaPath)
		if err != nil {
			s.releaseDownloadLock(entry.URL, gate)
			continue
		}
		var recheckEntry cacheEntry
		if json.Unmarshal(recheck, &recheckEntry) != nil || !recheckEntry.expired(now) || recheckEntry.URL != entry.URL {
			s.releaseDownloadLock(entry.URL, gate)
			continue
		}
		binPath := strings.TrimSuffix(metaPath, ".json") + ".bin"
		// 先删 data 再删 meta：meta 是条目可达性的事实源，先删 meta 后被杀
		// 会留下不可达的孤儿 .bin。
		_ = os.Remove(binPath)
		_ = os.Remove(metaPath)
		s.releaseDownloadLock(entry.URL, gate)
	}
	// 无 meta 的孤儿 .bin（历史删除中断、或旧版本先删 meta 后删 data 被杀）：
	// mtime 超过 2×TTL 才清，刚发布的条目不受影响。
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".bin") {
			continue
		}
		metaPath := filepath.Join(s.cacheDir, strings.TrimSuffix(name, ".bin")+".json")
		if _, err := os.Stat(metaPath); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < 2*s.cacheTTL {
			continue
		}
		_ = os.Remove(filepath.Join(s.cacheDir, name))
	}
	s.sweepStaleTmpFiles(now)
}

// sweepStaleTmpFiles 回收 CreateTemp 的 media-*.tmp 写盘中转：进程被杀/机器
// 重启打断的下载会留下最多 maxCacheEntrySize 的临时文件。只碰 .tmp，不触碰
// .bin/.json——review 截图面的目录同时是持久存储（见 main.go），它的过期
// 条目不回收，但中断下载的 tmp 残留同样要清，所以这是独立方法。mtime 超过
// 2×TTL 才清，正在写盘的文件因为 mtime 新不会被误删。
func (s *ProxyService) sweepStaleTmpFiles(now time.Time) {
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "media-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < 2*s.cacheTTL {
			continue
		}
		_ = os.Remove(filepath.Join(s.cacheDir, name))
	}
}

// restoreGoneBin 把隔离的旧字节原子放回 binPath。os.Link 不会覆盖已存在的
// 目标：写侧恰好在该窗口发布了新 data 时 link 失败（EEXIST），旧字节直接
// 丢弃——先 Stat 后 Rename 的两步恢复有 TOCTOU 窗口，会拿旧字节盖掉新 data
// （不变量第 4 条：不能安全恢复就丢弃，绝不覆盖新条目）。
func restoreGoneBin(goneBin, binPath string) {
	if err := os.Link(goneBin, binPath); err != nil {
		_ = os.Remove(goneBin)
		return
	}
	_ = os.Remove(goneBin)
}

// legacyScopeMarker scope 进缓存键之前的存量条目不可信标记：老条目的 meta
// 没有 Scope 字段，无法区分是匿名拉取的公开内容还是安装令牌拉取的私有附
// 件——后者若继续按匿名键命中，知道 URL 就能经 /api/github/media-proxy 跨
// 安装取到私有内容。构造服务时清一次：媒体代理面标记缺失则清光目录下的
// 缓存文件（公开内容重拉即可，私有内容宁可重拉不可误给）；review 截图面
// 是持久存储，只建目录并补写标记，不删、不拒读任何条目。
const legacyScopeMarker = ".cache-scope-v2"

// purgeLegacyCacheEntries 旧 scope 标记缺失时的启动清场。只有媒体代理面
// 允许整目录清空（guardedPurge）：review 截图面的目录是 review_assets 的持
// 久存储（无副本、ensure 不会重下），删任何条目等于把历史审查截图变成永
// 久 404——它只建目录并补写标记，不删、不拒读。标记 stat 出错（非 ENOENT
// 的 EIO/权限）两个面都跳过——读不出标记不等于标记不存在，宁可不清理也
// 不能误删持久资产。
func (s *ProxyService) purgeLegacyCacheEntries(guardedPurge bool) {
	_, statErr := os.Stat(filepath.Join(s.cacheDir, legacyScopeMarker))
	if statErr == nil {
		return
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return
	}
	if !guardedPurge {
		if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
			return
		}
		// 标记写失败不静默落定：下次启动重扫一遍，标记缺失期间无 Scope 的
		// 旧条目照常按匿名键命中（跨授权域风险由键隔离与接口鉴权兜住）。
		_ = os.WriteFile(filepath.Join(s.cacheDir, legacyScopeMarker), []byte("scope-v2\n"), 0o640)
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
// 前校验解析 IP。bearerGate 判定跳转目标是否仍可携带 Authorization：
// Go http.Client 会把敏感头透传给同域及子域目标，GHE 部署的兄弟子域
// （x.ghe.corp.com 对 ghe.corp.com）也在其列——目标过不了闸门就剥掉，
// nil 一律剥。Transport 克隆自 DefaultTransport，保留连接池参数。
func newGuardedClient(requireHTTPS bool, allowURL func(*url.URL) bool, guardAddress bool, trustedPrivateHosts []string, trustedHostsFunc func() []string, bearerGate func(*url.URL) bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if guardAddress {
		// 必须自己拨到目标才能校验目标 IP：走环境代理时校验到的会是代理地址
		// （公网代理→防线失效，回环代理→全部误拒）。代价是这条下载面不支持
		// HTTPS_PROXY 等环境代理；GitHub 媒体面不 guard，仍走环境代理。
		transport.Proxy = nil
		guarded := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   guardDialControl,
		}
		trusted := normalizeTrustedPrivateHosts(trustedPrivateHosts)
		if len(trusted) > 0 || trustedHostsFunc != nil {
			// 名单内主机跳过 IP 校验：判定用 DialContext 收到的 addr 主机名
			// （URL 的 host 部分，DNS 解析之前），不看解析出的 IP——放行粒度
			// 是主机名，同一内网地址上反代挂着的其他服务不会被连带放行。
			// 名单外主机、以及名单内主机 302 跳出的每次新建连仍走 guarded
			// 照常校验。trustedHostsFunc 每次拨号现取：新配的集成主机无需
			// 重启即生效。
			// 动态名单（allow_private_network 集成主机）不裸放行走私网口径
			// 守卫——与 Forgejo API 面的 AllowPrivateNetwork 同策略：放行
			// RFC1918/ULA/CGNAT，但回环、链路本地（含云元数据）、多播、
			// 未指定与保留段仍恒拒。静态名单保留裸放行（管理员显式声明，
			// UIV 等回环/私网服务靠它）。
			direct := &net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}
			privateMode := &net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				Control:   guardPrivateDialControl,
			}
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if host, _, err := net.SplitHostPort(addr); err == nil {
					host = strings.ToLower(host)
					if _, ok := trusted[host]; ok {
						return direct.DialContext(ctx, network, addr)
					}
					// 动态名单按 host:port 匹配：集成基址归一带端口（默认
					// 端口按其 scheme 补），私网口径守卫只放到配置过的
					// 端口——同主机其他端口上的服务不被连带信任。
					if trustedHostsFunc != nil && dynamicTrustedAddrMatch(addr, trustedHostsFunc()) {
						return privateMode.DialContext(ctx, network, addr)
					}
				}
				return guarded.DialContext(ctx, network, addr)
			}
		} else {
			transport.DialContext = guarded.DialContext
		}
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
			if req.Header.Get("Authorization") != "" &&
				(bearerGate == nil || !bearerGate(req.URL)) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

// dynamicTrustedAddrMatch 动态名单的拨号地址判定：名单项是 host:port
// 规范键（默认端口由集成侧按其基址 scheme 补好），拨号地址规范化成同
// 键后完全一致比较——放行粒度是配置过的端口，主机名相同但端口不同不
// 算数（同一内网主机上挂着的其他服务不会被连带放行）。
func dynamicTrustedAddrMatch(addr string, hosts []string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	hostPort := net.JoinHostPort(strings.ToLower(host), port)
	for _, h := range hosts {
		if strings.ToLower(strings.TrimSpace(h)) == hostPort {
			return true
		}
	}
	return false
}

// normalizeTrustedPrivateHosts 配置的名单规整成小写主机名集合：去空白、
// 统一小写、丢弃空项。匹配在 DialContext 收到的主机名上做完全一致比较
// ——不写端口、不支持通配符；IP 字面量写进名单同样按字符串匹配，只放行
// 该地址的直连，不是 IP 段放行。
func normalizeTrustedPrivateHosts(hosts []string) map[string]struct{} {
	if len(hosts) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = struct{}{}
		}
	}
	return set
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

// guardPrivateDialControl 私网放行口径的拨号校验（allow_private_network
// 集成主机专用）：复用 forges 包的同一判定源——RFC1918/ULA/CGNAT 放行，
// 回环、链路本地（含云元数据）、多播、未指定与保留段恒拒。报错统一落
// ErrBlockedUpstreamAddress 保持下游分类一致。
func guardPrivateDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("解析上游地址 %q 失败: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("上游地址 %q 不是合法 IP", host)
	}
	if forges.IsBlockedPrivateMode(ip) {
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
	return ProxyPath + "?url=" + url.QueryEscape(normalized) + "&" + ProxyURLVersionParam + "=" + ProxyURLVersion
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

	gate := s.acquireDownloadLock(normalized)
	select {
	case gate.sem <- struct{}{}:
	case <-ctx.Done():
		// 同 URL 已有下载在跑：等待必须吃 ctx。探测的 30s 时限是总时限，
		// 不能被同 URL 前一请求的 45s 客户端超时拖穿。等待失败未持有闸，
		// 用 abandon（不 <-lock）只尝试回收空闲条目。
		s.abandonDownloadLock(normalized, gate)
		return nil, ctx.Err()
	}
	defer s.releaseDownloadLock(normalized, gate)

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
	// Bearer 令牌是仓库凭证，只对 GitHub 媒体域（含部署实例的附件路径）
	// 有意义；截图面放行任意公网 host，把令牌发给外部图床等于把 App 安装凭证
	// 送出去。
	if bearer != "" && IsGitHubMediaURLFor(normalized, s.githubWebHost) {
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

	// 扩展名回退取重定向之后的最终 URL（resp.Request.URL）：client.Do
	// 可能跟随了白名单内的 302，a.png 跳到 b.svg 且最终类型是
	// octet-stream 时，借原始扩展名放行就会把 svg 字节按 png 回给客户端。
	finalURL := normalized
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType, ok := s.normalizeResponseContentType(resp.Header, finalURL)
	if !ok {
		return nil, s.mediaRejectionError(resp.Header, finalURL)
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

	// 内容类型闸门与整文件下载同口径：Range/HEAD 直通不能成为 svg/HTML
	// 准入的旁路（INT-171）。HEAD 响应常无 Content-Type，走扩展名回退
	// （集合里没有 .svg）——回退用重定向之后的最终 URL，与整文件路径
	// 同一口径。拒收时关掉 body 归还连接。
	finalURL := normalized
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	normalizedType, ok := s.normalizeResponseContentType(resp.Header, finalURL)
	if !ok {
		resp.Body.Close()
		return nil, s.mediaRejectionError(resp.Header, finalURL)
	}

	header := s.cloneRelevantHeaders(resp.Header)
	// Content-Type 只保留规范化后的那一个值：多值/逗号拼接已在闸门上
	// 被拒，octet-stream/空类型由扩展名回退改写成具体媒体类型。
	header.Set("Content-Type", normalizedType)
	return &PassThroughResponse{
		StatusCode: resp.StatusCode,
		Header:     header,
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

	// 面边界（不变量第 1 条）：review 截图面的「不可丢弃」不只在 meta 可读
	// 时成立。meta 读失败/损坏而 .bin 还在时，若按 miss 返回，后续重下会
	// rename 覆盖持久资产（上游已删/转私则探测失败，视觉证据被静默换掉，DB
	// 的 size/mime 仍留旧值）。该面按命中兜底：大小取 stat，类型用
	// http.DetectContentType 判（媒体面不做这条路——它的 miss 重下是自愈语
	// 义，覆盖无妨）。
	reviewFace := s.cacheSubdir != defaultCacheSubdir

	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if reviewFace {
			return s.servePersistedData(normalized, cachePath)
		}
		// ErrNotExist 是正常 miss；其余读失败（权限、目录损坏）同样按 miss——
		// 读路径不删文件（见下），下次读取还是 miss，重下后按同一 key 覆盖。
		// 目录级故障会退化成每条请求都重下，按限频 warn 留痕。
		if !errors.Is(err, os.ErrNotExist) {
			s.logMetaReadFail(metaPath, err)
		}
		return nil, false, nil
	}

	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		// meta 损坏（可能撞上写侧 rename 与 WriteFile 之间的中间态）按 miss
		// 处理，不在读路径删文件：删除会与同 key 写盘竞态，最坏抹掉刚发布的
		// 新条目。写侧发布完成后 meta 是完整 JSON，本次 miss 重下即自愈。
		// review 截图面例外：.bin 在就按命中兜底（同上，重下会覆盖持久资产）。
		if reviewFace {
			return s.servePersistedData(normalized, cachePath)
		}
		return nil, false, nil
	}

	// 授权域不匹配的兜底检查：scope 已进缓存键，正常不会同键撞域；meta 里
	// 的 Scope 字段是双保险（如旧格式条目或键派生变化时仍不误命中）。
	if entry.Scope != scope {
		return nil, false, nil
	}

	// 面边界（不变量第 1 条）：TTL 与退役只作用于媒体代理面。review 截图面
	// 的 .bin 是持久资产，过期不触发重下——重下会在上游内容变化时覆盖旧资
	// 产（DB 的 size/mime 不随之更新），上游删除/转私时历史截图会永久不可
	// 用。该面命中即返回持久条目；类型复核同样跳过：旧条目类型过不了当前
	// 准入时按重下处理会把持久资产换成新字节，与「不可丢弃」矛盾（这类条
	// 目响应带 nosniff + CSP sandbox，不构成脚本执行面）。
	if s.cacheSubdir == defaultCacheSubdir {
		// 过期条目只按 miss 处理，不在读路径删文件——与下面的内容复核同一
		// 约束：GetCached 不持下载锁，这里的 os.Remove 会与正在写盘的同 key
		// 下载竞态，最坏删掉别人刚发布的新条目。过期条目的退役路径：下次
		// 读取按 miss 触发重下，fetchAndCache 持下载锁写盘时按同一 key 覆盖
		// meta/data。
		if entry.expired(time.Now().UTC()) {
			return nil, false, nil
		}

		// 内容类型按当前准入规则复核：旧准入口径（收 svg）写进缓存的历史条
		// 目命中时按 miss 走重下，不靠 bump 缓存标记全量清目录（INT-171）。
		// 不过关只返回 miss，不在读路径删文件：GetCached 不持下载锁，这里删
		// meta/data 会与正在写盘的同 key 下载竞态，最坏删掉别人刚发布的新条
		// 目。下一次成功下载按同一 key 覆盖 meta/data，不再被重下的由目录扫
		// 描回收。meta 是单值小文件，多值歧义只在响应 header 判定里出现，这
		// 里直接走单值判定。旧条目 ContentType 剥参小写后为空或 octet-stream
		// 时保守判 miss：它们的放行依赖扩展名，而扩展名要用重定向后的最终
		// URL 才算得对，meta 里没存最终 URL——宁可贵一次重下，不做「按原始
		// URL 的扩展名放行」这种与写入侧不一致的复核（svg 字节会以回填的
		// image/png 命中，一个 TTL 内都错）。剥参后比较：旧写入路径原样存上
		// 游值，`octet-stream; charset=binary` 这类带参数形态也要拦住。
		baseType := strings.ToLower(strings.TrimSpace(strings.Split(entry.ContentType, ";")[0]))
		if baseType == "" || baseType == "application/octet-stream" {
			return nil, false, nil
		}
		normalizedType, allowed := s.normalizeMediaContentType(entry.ContentType, normalized)
		if !allowed {
			return nil, false, nil
		}
		// 走到这里的是带具体基础类型的条目：老缓存可能存着大写或带参数的原
		// 始值，返回值改成规范化类型（小写去参）；空/基础类型为 octet-stream
		// 的旧条目已在上面判 miss。CacheControl 同理：旧条目存的是 public（代
		// 理地址带 token，不该进共享缓存层），下发时一律覆写 private。只改返
		// 回值，不重写 meta 文件。
		entry.ContentType = normalizedType
		entry.CacheControl = buildCacheControl(s.cacheTTL)
	} else {
		// 持久面：类型原样下发（老条目可能是 octet-stream/带参数原始值），
		// Cache-Control 仍统一 private。
		entry.CacheControl = buildCacheControl(s.cacheTTL)
	}

	info, err := os.Stat(cachePath)
	if err != nil {
		// meta 在而 data 缺失/读不到（可能撞上写侧两步发布的中间态）：只按
		// miss，不在读路径删文件，重下自愈。
		return nil, false, nil
	}

	entry.Size = info.Size()
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, true, nil
}

// acquireDownloadLock 返回该 URL 的下载闸（容量 1 信号量）：发送成功即持有，
// 下载完成后 <-lock 收回。条目创建在 s.mu 下做，是否等待、等多久由调用方
// 结合 ctx 决定。调用方用完应调 releaseDownloadLock：空闲（无人持有、无人
// 等待）时把条目从 map 摘除，避免 map 随「历史上出现过的 URL 数」无界增长。
func (s *ProxyService) acquireDownloadLock(key string) *downloadGate {
	s.mu.Lock()
	defer s.mu.Unlock()

	gate, ok := s.download[key]
	if !ok {
		gate = &downloadGate{sem: make(chan struct{}, 1)}
		s.download[key] = gate
	}
	gate.refs++
	return gate
}

// releaseDownloadLock 归还闸（调用方持有成功后用）并尝试回收空闲条目：引用
// 计数归零才从 map 摘除——计数在 s.mu 下增减，等待者（acquire 时已登记）不
// 会让持有者的释放把闸变成孤儿。
func (s *ProxyService) releaseDownloadLock(key string, gate *downloadGate) {
	<-gate.sem
	s.mu.Lock()
	defer s.mu.Unlock()
	gate.refs--
	if gate.refs <= 0 && s.download[key] == gate {
		delete(s.download, key)
	}
}

// abandonDownloadLock 等待闸失败（ctx 超时/到期）时用：调用方从未持有闸，
// 不能 <-gate.sem（会阻塞到持有者释放），只递减计数、归零时回收。
func (s *ProxyService) abandonDownloadLock(key string, gate *downloadGate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate.refs--
	if gate.refs <= 0 && s.download[key] == gate {
		delete(s.download, key)
	}
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

// servePersistedData review 截图面的持久兜底：meta 不可读时，.bin 存在就按
// 命中返回——大小取 stat，类型用 http.DetectContentType（读文件头 512 字
// 节）兜底；.bin 也不在才是真 miss。Cache-Control 统一 private（地址带用户
// 凭证，不进共享缓存层）。
func (s *ProxyService) servePersistedData(normalized, cachePath string) (*CachedMedia, bool, error) {
	info, err := os.Stat(cachePath)
	if err != nil {
		return nil, false, nil
	}
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, false, nil
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if n == 0 {
		return nil, false, nil
	}
	contentType := http.DetectContentType(head[:n])
	if !strings.HasPrefix(contentType, "image/") {
		// 兜底类型不是位图：这一条按 miss（不让未知字节以持久资产名义流出），
		// 但同样不在读路径删文件。
		return nil, false, nil
	}
	return &CachedMedia{
		Metadata: cacheEntry{
			URL:          normalized,
			ContentType:  contentType,
			Size:         info.Size(),
			CachedAt:     info.ModTime().UTC(),
			CacheControl: buildCacheControl(s.cacheTTL),
		},
		Path: cachePath,
	}, true, nil
}

func (s *ProxyService) cloneRelevantHeaders(header http.Header) http.Header {
	out := make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		for _, value := range header.Values(key) {
			out.Add(key, value)
		}
	}
	// Cache-Control 不透传上游值：直通路径与缓存命中同一口径，一律 private
	// （代理地址带用户 JWT，上游的 public/max-age 会让共享缓存/CDN 把带
	// token 的 URL 存进共享层）。TTL 用服务实例的 cacheTTL，与命中路径同源
	// ——写死默认值会让非默认 TTL 时浏览器的 max-age 比服务端条目晚过期。
	out.Set("Cache-Control", buildCacheControl(s.cacheTTL))
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

// githubHost 公共 GitHub 的 Web 域；部署未配置 GHE 时的默认 Web 地址。
const githubHost = "github.com"

// attachmentPathPrefix GitHub 附件直链的路径形态：
// /user-attachments/assets/<id>（拖入/粘贴附件，通常无文件名扩展名）。
const attachmentPathPrefix = "/user-attachments/assets/"

// normalizeAuthority 统一 authority（host[:port]）的口径：去空白、小写，按
// scheme 去掉默认端口（https 443 / http 80）——配置与 URL 两侧都比同一形态，
// 否则「配置带端口、URL 不带」会被判成不同实例，或反过来把同 host 的另一个
// 服务当成同一实例。https 上的 :80 不是默认端口，保留。
func normalizeAuthority(scheme, authority string) string {
	a := strings.ToLower(strings.TrimSpace(authority))
	if host, port, err := net.SplitHostPort(a); err == nil {
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			return host
		}
	}
	return a
}

// isGitHubAttachmentPath 路径是否附件直链：解码后的路径以附件前缀开头且是
// 规范形态（不含 "."/".." 段、不重复斜杠、不以斜杠结尾）。只看前缀不够——
// /user-attachments/assets/../../other 这类写法在上游规范化后会离开附件目录，
// 凭证闸门必须把它拒掉。
func isGitHubAttachmentPath(u *url.URL) bool {
	p := u.Path // 已解码：%2e%2e 也是 ".."
	if !strings.HasPrefix(p, attachmentPathPrefix) {
		return false
	}
	return strings.TrimPrefix(p, attachmentPathPrefix) != "" && path.Clean(p) == p
}

// GitHubWebHost 从 GitHub API base（config 的 github_app.api_base_url）推导
// 部署的 Web 地址（authority = host[:非默认端口]）：apiBase 为空、解析失败或
// 取不到 host 时按公共 GitHub 兜底；host 为 api.github.com 时对应 github.com；
// 其余（GHE 的 https://<host>[:port]/api/v3）Web 地址即该 host 与端口本身。
// 端口必须保留：凭证闸门按完整 authority 判定，丢掉端口会把安装令牌发给同
// host 上另一个服务。service 层把它传给 IsGitHubAttachmentURLFor /
// IsGitHubMediaURLFor，不要手填域名。
func GitHubWebHost(apiBase string) string {
	parsed, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil || parsed == nil {
		return githubHost
	}
	authority := normalizeAuthority(parsed.Scheme, parsed.Host)
	if authority == "" || authority == "api.github.com" {
		return githubHost
	}
	return authority
}

// normalizeGitHubWebHost webHost 入参的统一口径：去空白、小写、去默认端口，
// 空值按公共 GitHub 处理——与 GitHubWebHost 的兜底一致，调用方传 "" 与传
// "github.com" 语义相同。
func normalizeGitHubWebHost(webHost string) string {
	if h := normalizeAuthority("https", webHost); h != "" {
		return h
	}
	return githubHost
}

// IsGitHubMediaURLFor 带部署口径的 bearer 重试闸门：能否把 App 安装令牌发给
// 该上游。令牌属于一个 GitHub 实例，所以只发给本部署的实例：公共 GitHub
// 部署（webHost 为空或 github.com）沿用 IsGitHubMediaURL 的媒体域白名单；
// GHE 部署只放行实例自己的附件直链（https、authority 恰为 webHost、无
// userinfo、无 fragment、规范附件路径），公共 GitHub 域也不放行——那不是
// 本实例，令牌在那里也没有意义。外部图床与同 host 的其他路径一律不放行。
func IsGitHubMediaURLFor(raw, webHost string) bool {
	webHost = normalizeGitHubWebHost(webHost)
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if webHost == githubHost {
		if !isAllowedGitHubMediaURL(parsed) {
			return false
		}
		// github.com 上的附件路径同样要求规范形态：点段不能借路径前缀把令牌
		// 带出附件目录（与 GHE 分支同一口径）。
		if strings.ToLower(parsed.Hostname()) == githubHost {
			return isGitHubAttachmentPath(parsed)
		}
		return true
	}
	if normalizeAuthority(parsed.Scheme, parsed.Host) != webHost {
		return false
	}
	return isGitHubAttachmentPath(parsed)
}

// IsGitHubAttachmentURL URL 是否是公共 GitHub 的附件形态直链：github.com 的
// user-attachments/assets 附件，或 user-images.githubusercontent.com 用户
// 图片——GitHub 拖入/粘贴附件的标准形态，路径通常没有文件名扩展名。service
// 层收集 PR 描述裸贴链接时用它豁免「必须有位图扩展名」的要求；能否发 App
// 安装令牌另由 IsGitHubMediaURL / IsGitHubMediaURLFor 按「可发请求的地址」
// 把关，两个口径独立。公共 GitHub 固定口径；GHE 部署用
// IsGitHubAttachmentURLFor。
func IsGitHubAttachmentURL(raw string) bool {
	return IsGitHubAttachmentURLFor(raw, githubHost)
}

// IsGitHubAttachmentURLFor URL 是否是该部署 GitHub 实例的附件形态直链。
// 附件形态是「公共 GitHub 形态 ∪ 本部署实例形态」：公共形态（
// github.com/user-attachments/assets/…、user-images.githubusercontent.com）
// 在任何部署下都算——GHE 的 PR 引用公共 GitHub 截图完全合理，截图下载面
// 本来就允许任意公网图；本实例形态按 webHost（GitHubWebHost 推导）判定
// authority + 附件路径。host 按解析结果再判，前缀嵌在路径里的仿冒地址
// （evil.com/<webHost>/user-attachments/assets/x）不算。
func IsGitHubAttachmentURLFor(raw, webHost string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == githubHost && isGitHubAttachmentPath(parsed) {
		return true
	}
	if host == "user-images.githubusercontent.com" {
		return true
	}
	webHost = normalizeGitHubWebHost(webHost)
	if webHost == githubHost || normalizeAuthority(parsed.Scheme, parsed.Host) != webHost {
		return false
	}
	return isGitHubAttachmentPath(parsed)
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

// isAllowedContentType 内容类型准入：GitHub 媒体面收位图与视频；截图面
// （imagesOnly）只收位图——octet-stream 与空 Content-Type 时按扩展名回退。
// svg 两个面都拒（INT-171）：用户态媒体代理的响应字节以 Fast Ship 同源交付，
// svg 在新标签页打开就是同源文档、能执行脚本，视觉审查也只吃位图。拒收
// 两路并行——内容类型按 image/svg 前缀拒（标准写法 image/svg+xml 与非标准
// 变体 image/svg 一并覆盖），扩展名回退集合不含 .svg。
// 只答放不放行的薄封装；写缓存/回客户端前用 normalizeResponseContentType
// 拿规范化类型。
func (s *ProxyService) isAllowedContentType(contentType string, rawURL string) bool {
	_, ok := s.normalizeMediaContentType(contentType, rawURL)
	return ok
}

// normalizeMediaContentType 「准入 + 规范化」判定：上游类型原样是
// image/* / video/*（GitHub 媒体面；截图面只收 image/*）时放行，返回
// 小写去参后的类型本身；类型为空或 application/octet-stream 时按扩展名
// 回退，放行则返回扩展名表里的具体媒体类型，不把 octet-stream/空类型原样
// 回给客户端：具体类型是确定信息（缓存与响应同源），不依赖浏览器嗅探——
// 同源 <img> 实测仍会嗅探 octet-stream 并解码，但那是实现细节，不能当设计
// 前提，<video> 与其他客户端也不保证。注意这里只做类型准入，不校验媒体字节
// （伪装成 image/png 的 svg 字节仍会被回），可执行性靠准入 + attachment +
// CSP 的组合堵。rawURL 必须传重定向之后的最终 URL（调用方责任），借原始
// 扩展名放行重定向出去的不可执行文档才无从发生。
func (s *ProxyService) normalizeMediaContentType(contentType, rawURL string) (string, bool) {
	if contentType == "" {
		return mediaTypeForExtension(rawURL, s.imagesOnly)
	}

	baseType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	// svg 在位图/视频分支之前统一拒，两个面共用同一道判定。单值里含逗号
	// 是多值拼接后的歧义类型（规范上 Content-Type 只允许单值），无从规范化
	// 也拒。
	if strings.HasPrefix(baseType, "image/svg") || strings.Contains(baseType, ",") {
		return "", false
	}
	if s.imagesOnly {
		if strings.HasPrefix(baseType, "image/") {
			return baseType, true
		}
		if baseType == "application/octet-stream" {
			return mediaTypeForExtension(rawURL, true)
		}
		return "", false
	}
	if strings.HasPrefix(baseType, "image/") || strings.HasPrefix(baseType, "video/") {
		return baseType, true
	}
	if baseType == "application/octet-stream" {
		return mediaTypeForExtension(rawURL, false)
	}
	// text/html、application/xhtml+xml、text/plain 等落不到任何前缀分支，
	// 一律拒。
	return "", false
}

// normalizeResponseContentType header 级准入：resp.Header 有多个
// Content-Type 值视为不可识别类型直接拒（规范上只允许单值，多值拼不出确定
// 类型）；单个值里基础类型含逗号（如 `image/png, image/svg+xml`）同样拒。
// 参数部分的逗号不算歧义——`video/mp4; codecs="avc1.42E01E, mp4a.40.2"`
// 是合法写法，且回给客户端的本来就是去掉参数的基础类型。放行时返回规范化
// 后的单一类型（见 normalizeMediaContentType）。
func (s *ProxyService) normalizeResponseContentType(header http.Header, rawURL string) (string, bool) {
	values := header.Values("Content-Type")
	if len(values) > 1 {
		return "", false
	}
	value := ""
	if len(values) == 1 {
		value = strings.TrimSpace(values[0])
	}
	return s.normalizeMediaContentType(value, rawURL)
}

// mediaRejectionError 拒收原因分三档：内容类型是 image/svg* 是「按类型确定」
// 的拒绝（同一资源下次还是 svg），出口按较长 TTL 缓存；上游没给类型而扩展
// 名是 .svg 的判据依赖上游本次响应，只能按很短 TTL；其它非媒体类型
// （text/html 之类）可能是上游抽风时的拦截页/登录页/限流页，同一 URL 下一
// 次就可能正常，不能缓存。
func (s *ProxyService) mediaRejectionError(header http.Header, rawURL string) error {
	values := header.Values("Content-Type")
	if len(values) > 1 {
		return ErrUnsupportedUpstreamMedia
	}
	value := ""
	if len(values) == 1 {
		value = values[0]
	}
	baseType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	if strings.HasPrefix(baseType, "image/svg") {
		return ErrUnsupportedSVG
	}
	if (baseType == "" || baseType == "application/octet-stream") && urlPathExtension(rawURL) == ".svg" {
		return ErrUnsupportedSVGByExtension
	}
	return ErrUnsupportedUpstreamMedia
}

// mediaExtensions 扩展名 → 具体媒体类型：扩展名回退集合与回写缓存/响应的
// 类型同源一张表，两边各写一份迟早漂移。svg 是可在浏览器执行脚本的矢量
// 图，不在此列——两个面都不按扩展名收它（曾在 GitHub 媒体面的集合里，
// INT-171 起移除）。
var mediaExtensions = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".mp4": "video/mp4", ".mov": "video/quicktime",
	".webm": "video/webm", ".m4v": "video/x-m4v",
}

// mediaTypeForExtension 扩展名回退：命中返回表内具体媒体类型，未命中
// （含 .svg、无扩展名）拒。imagesOnly 时只认位图扩展名，视频按未命中处理。
func mediaTypeForExtension(rawURL string, imagesOnly bool) (string, bool) {
	mediaType, ok := mediaExtensions[urlPathExtension(rawURL)]
	if !ok || (imagesOnly && !strings.HasPrefix(mediaType, "image/")) {
		return "", false
	}
	return mediaType, true
}

func hasBitmapExtension(rawURL string) bool {
	_, ok := mediaTypeForExtension(rawURL, true)
	return ok
}

// mediaExtensionByType 具体媒体类型 → 扩展名（反向表，供下载文件名兜底）。
// 同一类型可能有多个扩展名（image/jpeg 对应 .jpeg 与 .jpg），按扩展名字典
// 序遍历取第一个，保证输出确定。
var mediaExtensionByType = func() map[string]string {
	exts := make([]string, 0, len(mediaExtensions))
	for ext := range mediaExtensions {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	byType := make(map[string]string, len(mediaExtensions))
	for _, ext := range exts {
		if _, ok := byType[mediaExtensions[ext]]; !ok {
			byType[mediaExtensions[ext]] = ext
		}
	}
	return byType
}()

// IsMediaFileExtension 扩展名（带点，如 ".png"）是否在媒体扩展名表里。出口判
// 断下载文件名的末段是否已经是能直接打开的媒体扩展名时用它：只看「有没有
// 点」不够——`screenshot.v2`、结尾一个点的名字都会被当成已有扩展名。
func IsMediaFileExtension(ext string) bool {
	_, ok := mediaExtensions[strings.ToLower(ext)]
	return ok
}

// MediaTypeForExtension 扩展名反查具体媒体类型（未命中返回空串）。出口用它
// 对账「URL 后缀与响应类型是否一致」：后缀是上游可控的，`.png` 结尾却回
// video/mp4 的资源不能只凭后缀保留文件名。
func MediaTypeForExtension(ext string) string {
	return mediaExtensions[strings.ToLower(ext)]
}

// MediaFileExtension 由具体媒体类型反查扩展名（带点）。只认准入用的那张扩展名
// 表；表外类型（`image/avif`、`video/x-matroska` 这些按前缀放行但表里没有的）
// 返回空串，由调用方决定兜底——不能拿一个 `.bin` 无条件覆盖上游自己的
// `.avif`/`.mkv`，那会把本来能打开的文件改成打不开的。
func MediaFileExtension(contentType string) string {
	if ext, ok := mediaExtensionByType[strings.ToLower(strings.TrimSpace(contentType))]; ok {
		return ext
	}
	return ""
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

// buildCacheControl 媒体成功响应的缓存头。private 而非 public：代理地址里带
// 用户 JWT（?token=），public 允许前置共享缓存/CDN 把带 token 的 URL 存进共
// 享层；private 下浏览器私有缓存行为不变，token 不再多活一份。
func buildCacheControl(ttl time.Duration) string {
	seconds := int(ttl / time.Second)
	if seconds <= 0 {
		seconds = int(defaultCacheTTL / time.Second)
	}
	return "private, max-age=" + strconv.Itoa(seconds)
}

func cacheKey(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

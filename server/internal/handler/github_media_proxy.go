package handler

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

type GitHubMediaProxyHandler struct {
	service *githubmedia.ProxyService
}

func NewGitHubMediaProxyHandler(service *githubmedia.ProxyService) *GitHubMediaProxyHandler {
	return &GitHubMediaProxyHandler{service: service}
}

func (h *GitHubMediaProxyHandler) Proxy(c *gin.Context) {
	rawURL := decodeMediaURLParam(c.Query("url"))
	rangeHeader := c.GetHeader("Range")
	if strings.Contains(rangeHeader, ",") {
		// 多段 Range 忽略：上游对它会回 multipart/byteranges，那既不是图片
		// 也不是视频，内容类型闸门必然拒；RFC 7233 允许服务器忽略 Range，
		// 按无 Range 走整文件路径返回 200 完整内容，比 415 更符合预期。
		// 同时要删掉请求上的头：缓存命中走 http.ServeContent，它会自己再
		// 解析 Range，不删会在这里又变回 206 multipart。
		rangeHeader = ""
		c.Request.Header.Del("Range")
	}
	headOnly := c.Request.Method == http.MethodHead

	if cached, ok, err := h.service.GetCached(rawURL); err != nil {
		writeMediaProxyError(c, err)
		return
	} else if ok && serveCachedMedia(c, cached) {
		return
	}
	// 命中但文件打不开（条目刚被过期清扫或人工删除掉，GetCached 读到 meta
	// 与 os.Open 之间没有互斥）：按 miss 走重下，这个窗口不暴露成破图 404。
	// 容量机制不删任何已落盘条目（闸长在写入点，见 githubmedia 的
	// cacheReservation），所以窗口只来自过期清扫——历史上那套「扫描期按最旧
	// 淘汰」曾在 INT-172 R4 方向审计后被删除，它才会把窗口扩大到任意旧条目。

	if rangeHeader != "" || headOnly {
		resp, err := h.service.ProxyPass(c.Request.Context(), rawURL, rangeHeader, headOnly)
		if err != nil {
			writeMediaProxyError(c, err)
			return
		}
		servePassThrough(c, rawURL, resp, headOnly)
		return
	}

	cached, err := h.service.FetchAndCache(c.Request.Context(), rawURL)
	if err == nil {
		if serveCachedMedia(c, cached) {
			return
		}
		// 刚下完就打不开是文件系统级故障，与淘汰窗口无关：保持原有 404。
		c.String(http.StatusNotFound, "cached media not found")
		return
	}

	// 本地拒绝缓存两种成因走同一条退路——把流式响应直接回给客户端：单文件
	// 超限（ErrMediaTooLarge）、媒体面缓存水位会越过容量上限（ErrCacheFull，
	// 容量机制不删已落盘条目，所以只拒新字节）。上游自己回的 413 不是本地
	// 拒绝，走下面的常规错误分支。
	if errors.Is(err, githubmedia.ErrMediaTooLarge) || errors.Is(err, githubmedia.ErrCacheFull) {
		// 拒绝点在上游响应打开之后（内容类型闸门已过）：直接复用它，不再发
		// 第二次上游请求——容量满不是偶发而是稳态，超限附件也会每次下两遍，
		// 二次 GET 都会让上游请求数与连接数翻倍。
		if resp, ok := githubmedia.PassthroughResponse(err); ok {
			servePassThrough(c, rawURL, resp, false)
			return
		}
		resp, passErr := h.service.ProxyPass(c.Request.Context(), rawURL, "", false)
		if passErr != nil {
			writeMediaProxyError(c, passErr)
			return
		}
		servePassThrough(c, rawURL, resp, false)
		return
	}

	writeMediaProxyError(c, err)
}

func decodeMediaURLParam(value string) string {
	decoded := strings.TrimSpace(html.UnescapeString(value))
	for range 2 {
		next, err := url.QueryUnescape(decoded)
		if err != nil || next == decoded {
			break
		}
		decoded = next
	}
	return decoded
}

// servePassThrough 把直通响应写回客户端：拷贝相关头、补硬化头与下载文件名、
// 回状态码、流式拷 body。headOnly 时不拷 body（上游 HEAD 本来就没有）。
func servePassThrough(c *gin.Context, rawURL string, resp *githubmedia.PassThroughResponse, headOnly bool) {
	defer resp.Body.Close()

	copyMediaHeaders(c.Writer.Header(), resp.Header)
	// 传规范化后的类型：直通回包的 Content-Type 已是去参小写的基础类型，
	// 文件名对账用它，别用上游原始值（带参数会对不上表）。
	setMediaProxyHardeningHeaders(c.Writer.Header(), rawURL, resp.Header.Get("Content-Type"))
	c.Status(resp.StatusCode)
	if headOnly {
		return
	}
	_, _ = io.Copy(c.Writer, resp.Body)
}

func serveCachedMedia(c *gin.Context, media *githubmedia.CachedMedia) bool {
	file, err := os.Open(media.Path)
	if err != nil {
		// 打不开（文件刚被回收/磁盘故障）：不写任何响应，让调用方按 miss
		// 回退（重下）或按缓存丢失回 404。
		return false
	}
	defer file.Close()

	header := c.Writer.Header()
	header.Set("Content-Type", media.Metadata.ContentType)
	// 不透传 meta 里存的原始缓存头：旧条目存的是 public（代理地址带 token，
	// 不该进共享缓存层）。getCachedNormalized 已把它规范化成 private 且与
	// 服务端条目 TTL 同源，直接用——写死 max-age 会在 cacheTTL 非默认时让
	// 浏览器比服务端晚过期。
	header.Set("Cache-Control", media.Metadata.CacheControl)
	header.Set("Accept-Ranges", "bytes")
	if media.Metadata.ETag != "" {
		header.Set("ETag", media.Metadata.ETag)
	}
	if media.Metadata.LastModified != "" {
		header.Set("Last-Modified", media.Metadata.LastModified)
	}
	// 在 http.ServeContent 之前设置：它只补 Content-Type/Length/Range 等头，
	// 已显式设置的头不会被覆盖（见下），硬化头也原样保留。
	setMediaProxyHardeningHeaders(header, media.Metadata.URL, media.Metadata.ContentType)

	http.ServeContent(c.Writer, c.Request, "", media.Metadata.CachedAt, file)
	return true
}

// setMediaProxyHardeningHeaders 媒体代理成功响应的统一硬化头（INT-171）。必须
// 在 copyMediaHeaders 之后、http.ServeContent 之前设置，避免被上游头覆盖。
// 硬化头与 disposition 走公共无缓存出口实现（INT-182 attachment 档）；档位
// Cache-Control 不在这里写——缓存面的 TTL 由 s.cacheTTL 派生，由调用方自己写。
func setMediaProxyHardeningHeaders(header http.Header, upstreamURL, contentType string) {
	egress.attachmentHeaders(header, mediaProxyFileName(upstreamURL, contentType))
}

// mediaProxyFileName 下载文件名：优先上游 URL 末段（GitHub 附件直链多半是没
// 扩展名的 uuid），且后缀要与响应类型对账——URL 后缀是上游可控的，不能只凭
// 「像个媒体扩展名」就信：`.png` 结尾却回 `video/mp4` 的资源，下载下来照旧
// 打不开。规则：
//   - 末段后缀与响应类型一致（后缀反查出的类型 == 响应类型）原样保留；
//   - 表内类型反查出确定扩展名，就用它替换不可信后缀（`screenshot.v2` →
//     `screenshot.png`、`trailing.` → `trailing.png`、伪装后缀 `x.png` 回
//     `video/mp4` → `x.mp4`）；
//   - 表外类型（`image/avif`、`video/x-matroska`）反查不到，保留上游自己的
//     `.avif`/`.mkv`——不能拿 `.bin` 覆盖掉本来能打开的文件。
func mediaProxyFileName(upstreamURL, contentType string) string {
	name := "media"
	if parsed, err := url.Parse(upstreamURL); err == nil {
		if base := sanitizeAttachmentFileName(path.Base(parsed.Path)); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	// 先去掉末段结尾的点：`a.png.` 的 path.Ext 是 "."，不去掉会拼成 a.png.png。
	if trimmed := strings.TrimRight(name, "."); trimmed != "" {
		name = trimmed
	}
	ext := path.Ext(name)
	// 后缀可信：在媒体表里、且它对应的类型就是响应类型（大小写不敏感，与
	// 准入侧同口径）。
	if ext != "" && githubmedia.IsMediaFileExtension(ext) &&
		strings.EqualFold(githubmedia.MediaTypeForExtension(ext), contentType) {
		return name
	}
	if want := githubmedia.MediaFileExtension(contentType); want != "" {
		return strings.TrimSuffix(name, ext) + want
	}
	if ext != "" {
		return name
	}
	return name + ".bin"
}

func copyMediaHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// svg 拒绝的浏览器缓存秒数，分两档：内容类型判定（image/svg*，同一资源下次
// 还是 svg）可以钉住——不给缓存头的话每次页面渲染都会重新请求代理、代理再
// 回源一趟才在闸门上拒；扩展名判定（上游没给类型 + .svg）的判据来自上游本
// 次响应——下次它可能给正常类型，只能按短 TTL，钉 10 分钟会把裂图窗口也钉
// 住。其它非媒体类型（text/html 之类可能是拦截页）不带缓存头。
const (
	mediaProxyRejectionMaxAge            = 600
	mediaProxyRejectionByExtensionMaxAge = 60
)

func writeMediaProxyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, githubmedia.ErrInvalidMediaURL):
		c.String(http.StatusBadRequest, "invalid github media url")
	case errors.Is(err, githubmedia.ErrUnsupportedSVGByExtension):
		// 顺序在 ErrUnsupportedSVG 之前：它包装了后者。判据来自上游本次响应，
		// 只按短 TTL。
		c.Header("Cache-Control", "private, max-age="+strconv.Itoa(mediaProxyRejectionByExtensionMaxAge))
		c.String(http.StatusUnsupportedMediaType, "unsupported media type")
	case errors.Is(err, githubmedia.ErrUnsupportedSVG):
		// 内容类型判定的 svg 是确定性拒绝，可按较长 TTL 缓存。
		c.Header("Cache-Control", "private, max-age="+strconv.Itoa(mediaProxyRejectionMaxAge))
		c.String(http.StatusUnsupportedMediaType, "unsupported media type")
	case errors.Is(err, githubmedia.ErrUnsupportedUpstreamMedia):
		c.String(http.StatusUnsupportedMediaType, "unsupported media type")
	default:
		var upstreamErr *githubmedia.UpstreamStatusError
		if errors.As(err, &upstreamErr) {
			status := upstreamErr.StatusCode
			if status >= http.StatusInternalServerError {
				status = http.StatusBadGateway
			}
			c.String(status, http.StatusText(status))
			return
		}
		c.String(http.StatusBadGateway, "failed to fetch github media")
	}
}

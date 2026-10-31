package handler

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
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
	} else if ok {
		serveCachedMedia(c, cached)
		return
	}

	if rangeHeader != "" || headOnly {
		resp, err := h.service.ProxyPass(c.Request.Context(), rawURL, rangeHeader, headOnly)
		if err != nil {
			writeMediaProxyError(c, err)
			return
		}
		defer resp.Body.Close()

		copyMediaHeaders(c.Writer.Header(), resp.Header)
		setMediaProxyHardeningHeaders(c.Writer.Header())
		c.Status(resp.StatusCode)
		if headOnly {
			return
		}
		_, _ = io.Copy(c.Writer, resp.Body)
		return
	}

	cached, err := h.service.FetchAndCache(c.Request.Context(), rawURL)
	if err == nil {
		serveCachedMedia(c, cached)
		return
	}

	// 本地超限（拉到的图超过缓存上限）转 ProxyPass 兜底，把流式响应直接回给
	// 客户端；上游自己回的 413 不是本地超限，走下面的常规错误分支。
	if errors.Is(err, githubmedia.ErrMediaTooLarge) {
		resp, passErr := h.service.ProxyPass(c.Request.Context(), rawURL, "", false)
		if passErr != nil {
			writeMediaProxyError(c, passErr)
			return
		}
		defer resp.Body.Close()

		copyMediaHeaders(c.Writer.Header(), resp.Header)
		setMediaProxyHardeningHeaders(c.Writer.Header())
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
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

func serveCachedMedia(c *gin.Context, media *githubmedia.CachedMedia) {
	file, err := os.Open(media.Path)
	if err != nil {
		c.String(http.StatusNotFound, "cached media not found")
		return
	}
	defer file.Close()

	header := c.Writer.Header()
	header.Set("Content-Type", media.Metadata.ContentType)
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
	setMediaProxyHardeningHeaders(header)

	http.ServeContent(c.Writer, c.Request, "", media.Metadata.CachedAt, file)
}

// setMediaProxyHardeningHeaders 媒体代理成功响应的统一硬化头（INT-171）。
// ?token= 会出现在地址栏，代理绝不能交付同源可执行文档：nosniff 阻止
// 浏览器嗅探改判；attachment 把「新标签页打开」降级成下载而不是同源导航
// ——只影响顶层导航，<img>/<video> 子资源渲染不受影响，可无条件加；
// CSP sandbox 兜底禁脚本禁同源。必须在 copyMediaHeaders 之后、
// http.ServeContent 之前设置，避免被上游头覆盖。
func setMediaProxyHardeningHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Disposition", "attachment")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
}

func copyMediaHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// mediaProxyRejectionMaxAge 415 的浏览器缓存秒数。被准入拒收的资源（如 Issue
// 正文里的 svg 图片）是确定性的：同一 URL 同一准入规则下永远拒。不给缓存头
// 的话每次页面渲染都会重新请求代理，代理再回源一趟（302 + 实体各一跳）才在
// 闸门上拒——升级前这类资源是命中磁盘的，不回源就是不给浏览器留缓存头造成
// 的放大。短 TTL 就够了：规则变了也不会被旧结果卡太久；不做服务端负缓存
// （规则变更时要清理墓碑，语义成本高于收益）。
const mediaProxyRejectionMaxAge = 600

func writeMediaProxyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, githubmedia.ErrInvalidMediaURL):
		c.String(http.StatusBadRequest, "invalid github media url")
	case errors.Is(err, githubmedia.ErrUnsupportedUpstreamMedia):
		c.Header("Cache-Control", "private, max-age="+strconv.Itoa(mediaProxyRejectionMaxAge))
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

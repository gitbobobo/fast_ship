package handler

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
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

	var upstreamErr *githubmedia.UpstreamStatusError
	if errors.As(err, &upstreamErr) && upstreamErr.StatusCode == http.StatusRequestEntityTooLarge {
		resp, passErr := h.service.ProxyPass(c.Request.Context(), rawURL, "", false)
		if passErr != nil {
			writeMediaProxyError(c, passErr)
			return
		}
		defer resp.Body.Close()

		copyMediaHeaders(c.Writer.Header(), resp.Header)
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

	http.ServeContent(c.Writer, c.Request, "", media.Metadata.CachedAt, file)
}

func copyMediaHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func writeMediaProxyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, githubmedia.ErrInvalidMediaURL):
		c.String(http.StatusBadRequest, "invalid github media url")
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

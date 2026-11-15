package github

import (
	"errors"
	"io"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v62/github"
)

// 本文件把 service 需要的 GitHub 错误分类下沉到 adapter：内部仍 errors.As 到
// go-github 的错误类型，service 只调这里的判定助手，不 import SDK。

// ErrorStatus 提取 GitHub 错误响应的 HTTP 状态码；非 ErrorResponse 或缺响应
// 时返回 false。
func ErrorStatus(err error) (int, bool) {
	var respErr *gh.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return 0, false
	}
	return respErr.Response.StatusCode, true
}

// ErrorStatusMessage 提取错误响应的 HTTP 状态码与 message 字段；非
// ErrorResponse 或缺响应时返回 false。批量关闭的错误规范化按「<状态码>
// <Message>」拼固定文案用到。
func ErrorStatusMessage(err error) (int, string, bool) {
	var respErr *gh.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return 0, "", false
	}
	return respErr.Response.StatusCode, respErr.Message, true
}

// IsRateLimitError 判定 GitHub 错误是否为 API 限流：*RateLimitError、
// *AbuseRateLimitError，或 403/429 且响应体含 rate limit/abuse detection 字样的
// *ErrorResponse。判定口径与 PR 域的 pullRequestAccessHint 对齐但不共用
// （那边是 githubapi.StatusError 的 PR 读取域）；Message 是响应体 JSON 解析出的
// message 字段，原始 body 由 go-github 解析后回填，可尽力重读。
func IsRateLimitError(err error) bool {
	var rateLimit *gh.RateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}
	var abuse *gh.AbuseRateLimitError
	if errors.As(err, &abuse) {
		return true
	}
	var respErr *gh.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return false
	}
	code := respErr.Response.StatusCode
	// 429 本身是权威限流信号，无需依赖 body 文案——空 body、纯 Retry-After
	// 或网关/WAF 自定义文案的 429 也必须触发熔断（INT-185 R13）。
	if code == http.StatusTooManyRequests {
		return true
	}
	if code != http.StatusForbidden {
		return false
	}
	text := strings.ToLower(respErr.Message)
	if raw := readErrorBody(respErr.Response.Body); raw != "" {
		text += " " + strings.ToLower(raw)
	}
	return strings.Contains(text, "rate limit") || strings.Contains(text, "abuse detection")
}

// readErrorBody 尽力读取错误响应体；已被读走或关闭时返回空串，不影响判定
// （还有 Message 兜底）。
func readErrorBody(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return ""
	}
	return string(raw)
}

// IsPATAccessError 判定 GitHub 错误是否为「PAT 无权访问该资源」：403 且
// message 含固定文案；非 ErrorResponse 时退化为错误串匹配（Error() 里已
// 包含响应文案）。
func IsPATAccessError(err error) bool {
	var errResp *gh.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.Response != nil && errResp.Response.StatusCode == http.StatusForbidden {
			return strings.Contains(strings.ToLower(errResp.Message), "resource not accessible by personal access token")
		}
	}

	return strings.Contains(strings.ToLower(err.Error()), "resource not accessible by personal access token")
}

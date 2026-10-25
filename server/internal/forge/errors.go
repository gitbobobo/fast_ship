package forge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// 错误面（INT-167）：service 层 import 这里的判定函数，各适配器的错误形态
// 在此归一。判定按 StatusCode 等结构特征识别而非类型身份——GitHub 适配器
// 原样透传 githubapi.StatusError（错误文本与行为不变），任务 3 的 Forgejo
// 适配器返回本包的 StatusError 同样命中同一组判定。

// ErrForbidden 平台拒绝操作（如缺 statuses:write 权限）。
var ErrForbidden = errors.New("forge: forbidden")

// ErrDiffTooLarge 客户端侧判定 diff 超本端读入上限的确定性失败（适配器本地
// 探测，平台并未拒绝——无 HTTP 状态码可言）。与平台的 too_large 响应归一到
// 同一判定：service 只认 IsDiffTooLarge，不区分两种来源。
var ErrDiffTooLarge = errors.New("forge: diff too large")

// StatusError 非 2xx 响应。字段与 githubapi.StatusError 同构，便于判定函数
// 在两种形态间按同一口径取值。
type StatusError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
	// RateLimited 由构造处按完整响应体预判定：Body 截断后限流字样可能落在
	// 被丢弃的中段（大 HTML 错误页），置位时 IsRateLimit 不再依赖 Body 文本。
	RateLimited bool
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("forge: %s %s -> %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// statusOf 取 err 链里平台错误的 HTTP 状态码：forge.StatusError 与 GitHub
// 适配器透传的 githubapi.StatusError 都识别。
func statusOf(err error) (int, bool) {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode, true
	}
	var ge *githubapi.StatusError
	if errors.As(err, &ge) {
		return ge.StatusCode, true
	}
	return 0, false
}

// IsForbidden 判断 err 是否 403（直接或包在链里）。
func IsForbidden(err error) bool {
	if code, ok := statusOf(err); ok {
		return code == http.StatusForbidden
	}
	return errors.Is(err, ErrForbidden) || errors.Is(err, githubapi.ErrForbidden)
}

// IsNotFound 判断 err 是否 404（评论被删后 PATCH 失败，回退到按标记找回）。
func IsNotFound(err error) bool {
	code, ok := statusOf(err)
	return ok && code == http.StatusNotFound
}

// IsUnauthorized 判断 err 是否 401（凭证缺失/吊销）。与 403 同属「这轮重试
// 也无意义」的确定性失败——防抖实况读与轮询按确定性口径用它归类。
func IsUnauthorized(err error) bool {
	code, ok := statusOf(err)
	return ok && code == http.StatusUnauthorized
}

// IsRateLimit 判断 err 是否限流响应（瞬时）：HTTP 429，或 403 且响应体含
// rate limit/abuse detection 字样——GitHub 主/二次限流正是回 403（含
// Retry-After），与权限 403 无法从状态码区分，识别口径与
// issue_pull_request.go 的 pullRequestAccessHint 一致。限流 403 若被归为
// 确定性失败会误删防抖行/误停用项目审查。
func IsRateLimit(err error) bool {
	code, ok := statusOf(err)
	if !ok {
		return false
	}
	if code == http.StatusTooManyRequests {
		return true
	}
	if code != http.StatusForbidden {
		return false
	}
	var se *StatusError
	var ge *githubapi.StatusError
	body := ""
	switch {
	case errors.As(err, &se):
		if se.RateLimited {
			return true
		}
		body = se.Body
	case errors.As(err, &ge):
		body = ge.Body
	}
	return bodySuggestsRateLimit(body)
}

// bodySuggestsRateLimit 响应体是否含限流说明字样。构造处对完整 body 调用
// （结论存 StatusError.RateLimited），判定函数对截断后 Body 再查一次兜底。
func bodySuggestsRateLimit(body string) bool {
	body = strings.ToLower(body)
	return strings.Contains(body, "rate limit") || strings.Contains(body, "abuse detection")
}

// IsDiffTooLarge 判断 err 是否「PR diff 超出上限」的确定性拒绝。两种来源：
// 平台 4xx+errors[code=too_large]（语义与 githubapi.IsDiffTooLarge 完全一致
// ——借同构字段转成 githubapi.StatusError 复用其判定，两份口径不会漂移），
// 或适配器本端截断探测的 ErrDiffTooLarge 哨兵。
func IsDiffTooLarge(err error) bool {
	if errors.Is(err, ErrDiffTooLarge) {
		return true
	}
	var ge *githubapi.StatusError
	if errors.As(err, &ge) {
		return githubapi.IsDiffTooLarge(err)
	}
	var se *StatusError
	if errors.As(err, &se) {
		return githubapi.IsDiffTooLarge(&githubapi.StatusError{
			StatusCode: se.StatusCode, Method: se.Method, URL: se.URL, Body: se.Body,
		})
	}
	return false
}

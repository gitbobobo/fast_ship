package forge

import (
	"errors"
	"fmt"
	"net/http"

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

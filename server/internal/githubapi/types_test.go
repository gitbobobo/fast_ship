package githubapi

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// IsDiffTooLarge 是行内 review 降级收尾的判定入口，误判两个方向都有代价：
// 把暂时性失败认成 too_large 会静默丢行内意见；把 too_large 认成可重试会让
// sync_error 永久挂账（GitHub 对超限 diff 的拒绝是确定性的）。所以边界必须
// 卡死：状态码 406/413/422 + 响应体结构化 errors[].code = too_large，缺一不可。
func TestIsDiffTooLarge(t *testing.T) {
	// 真实形态的超限响应体（GitHub 口径，社区案例同型）。
	tooLargeBody := `{"message":"Sorry, the diff exceeded the maximum number of lines (20000)","errors":[{"resource":"PullRequest","field":"diff","code":"too_large","message":""}]}`
	statusErr := func(code int, body string) *StatusError {
		return &StatusError{StatusCode: code, Method: "GET", URL: "/repos/o/r/pulls/78", Body: body}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"406+too_large", statusErr(http.StatusNotAcceptable, tooLargeBody), true},
		{"413+too_large", statusErr(http.StatusRequestEntityTooLarge, tooLargeBody), true},
		// 422 是 GitHub 结构化 errors[].code 的 canonical 状态码（validation failed）：
		// 若某条路径用 422+too_large 拒绝 diff，同样不能退避。
		{"422+too_large", statusErr(http.StatusUnprocessableEntity, tooLargeBody), true},
		// 上游经 fmt.Errorf("%w") 包装后仍要认出来（fetchInlineLines 的用法）。
		{"wrapped 406+too_large", fmt.Errorf("读取 diff 失败: %w", statusErr(http.StatusNotAcceptable, tooLargeBody)), true},
		// 406 是通用 Unacceptable：没有结构化 too_large 就不是超限，按可重试处理。
		{"406 无 errors", statusErr(http.StatusNotAcceptable, `{"message":"Unacceptable"}`), false},
		{"406 code 不是 too_large", statusErr(http.StatusNotAcceptable, `{"errors":[{"resource":"PullRequest","field":"diff","code":"unreadable"}]}`), false},
		{"406 body 非 JSON", statusErr(http.StatusNotAcceptable, "<html>gateway error</html>"), false},
		// 状态码不在白名单：即使 body 说 too_large 也不认（500 是暂时态，重试有意义）。
		{"500+too_large", statusErr(http.StatusInternalServerError, tooLargeBody), false},
		{"404+too_large", statusErr(http.StatusNotFound, tooLargeBody), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDiffTooLarge(tc.err); got != tc.want {
				t.Fatalf("IsDiffTooLarge(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	// errors.New 普通错误（无 *StatusError 在链上）同样不可判为超限。
	if IsDiffTooLarge(errors.New("上游 502")) {
		t.Fatal("无 StatusError 的错误不应判为 diff 超限")
	}
}

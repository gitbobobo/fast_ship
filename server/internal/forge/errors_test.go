package forge

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// TestIsRateLimit 限流判定：429 与「403 + 响应体 rate limit/abuse」是瞬时
// 限流；权限 403（无字样）、其它状态、非状态错误都不是。两种适配器错误
// 形态（forge.StatusError 与 githubapi.StatusError）同口径。
func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"forge 429", &StatusError{StatusCode: http.StatusTooManyRequests}, true},
		{"forge 403 rate limit", &StatusError{StatusCode: 403, Body: "API rate limit exceeded"}, true},
		{"forge 403 abuse", &StatusError{StatusCode: 403, Body: "You have triggered an abuse detection mechanism"}, true},
		{"forge 403 permission", &StatusError{StatusCode: 403, Body: "Forbidden"}, false},
		{"github 403 rate limit", &githubapi.StatusError{StatusCode: 403, Body: "rate limit"}, true},
		{"github 403 permission", &githubapi.StatusError{StatusCode: 403, Body: "Resource not accessible"}, false},
		{"forge 404", &StatusError{StatusCode: 404}, false},
		{"wrapped 429", fmt.Errorf("outer: %w", &StatusError{StatusCode: 429}), true},
		{"plain error", fmt.Errorf("dial tcp"), false},
	}
	for _, c := range cases {
		if got := IsRateLimit(c.err); got != c.want {
			t.Fatalf("%s: IsRateLimit = %v, want %v", c.name, got, c.want)
		}
	}
}

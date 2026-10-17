package github

import (
	"context"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// 编译期钉住 PRClient 满足 seam 契约。
var _ githubapi.GitHubClient = (*PRClient)(nil)

func TestStaticTokenAuth_ReturnsTokenAndInvalidateIsNoop(t *testing.T) {
	auth := staticTokenAuth{token: "tok"}
	got, err := auth.Bearer(context.Background(), 7)
	if err != nil || got != "tok" {
		t.Fatalf("Bearer = %q %v, want tok/nil", got, err)
	}
	auth.Invalidate(7) // 静态令牌无可作废缓存，必须为 no-op
	got, err = auth.Bearer(context.Background(), 7)
	if err != nil || got != "tok" {
		t.Fatalf("Bearer after invalidate = %q %v, want tok/nil", got, err)
	}
}

// 空 Token 匿名：Bearer 返回空串，REST 实现据此不发 Authorization 头。
func TestStaticTokenAuth_EmptyTokenIsAnonymous(t *testing.T) {
	got, err := staticTokenAuth{}.Bearer(context.Background(), 1)
	if got != "" || err != nil {
		t.Fatalf("anonymous Bearer = %q %v, want empty/nil", got, err)
	}
}

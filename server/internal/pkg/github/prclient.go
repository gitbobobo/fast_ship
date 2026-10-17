package github

import (
	"context"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// staticTokenAuth PRClient 的凭证提供者：恒返回同一 Token（空串即匿名请求），
// Invalidate 为 no-op——静态令牌没有可作废的缓存。
type staticTokenAuth struct {
	token string
}

func (a staticTokenAuth) Bearer(ctx context.Context, installationID int64) (string, error) {
	return a.token, nil
}

func (a staticTokenAuth) Invalidate(installationID int64) {}

// PRClient issue 关联 PR 读取（attach/sync）的 Token 实现：与 App 适配器共享
// githubapi 的 REST 代码，凭证取用户 Token 库（空 Token 匿名读公共 PR）。
type PRClient struct {
	githubapi.GitHubClient
}

// NewPRClient 用静态 Token 构造 PR 读取客户端。
func NewPRClient(token string) *PRClient {
	return &PRClient{GitHubClient: githubapi.NewClient(staticTokenAuth{token: token}, "https://api.github.com")}
}

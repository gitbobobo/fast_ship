package githubapp

import (
	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// Client PR 评审链路的安装令牌实现：薄壳，REST 调用与 401 重试收敛在共享的
// githubapi.NewClient，这里只负责把 App 安装令牌仓库接成 AuthProvider。
type Client struct {
	githubapi.GitHubClient
}

func NewClient(store *InstallationTokenStore) *Client {
	return &Client{GitHubClient: githubapi.NewClient(store, store.baseURL)}
}

// 编译期钉住薄壳仍满足 seam 契约。
var _ githubapi.GitHubClient = (*Client)(nil)

package githubapp

import (
	"context"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// Client PR 评审链路的安装令牌实现：薄壳，REST 调用与 401 重试收敛在共享的
// githubapi.NewClient，这里只负责把 App 安装令牌仓库接成 AuthProvider。
type Client struct {
	githubapi.GitHubClient
	store *InstallationTokenStore
}

func NewClient(store *InstallationTokenStore) *Client {
	return &Client{GitHubClient: githubapi.NewClient(store, store.baseURL), store: store}
}

// GetRepository 仓库可访问性检查（INT-135 前置条件）：installationID=0 时先
// 用 App JWT 查 /repos/{o}/{r}/installation 解析仓库所属安装，再以安装令牌
// 调 GET /repos/{o}/{r}——一步完成「App 已安装且令牌可访问仓库」的判断。
// 已知安装 id（webhook 链路）直接走底层调用，省一次解析。
func (c *Client) GetRepository(ctx context.Context, installationID int64, repoFullName string) error {
	if installationID == 0 {
		resolved, err := c.store.InstallationForRepo(ctx, repoFullName)
		if err != nil {
			return err
		}
		installationID = resolved
	}
	return c.GitHubClient.GetRepository(ctx, installationID, repoFullName)
}

// ResolveInstallation 按仓库名解析 App 安装 id：复用 InstallationForRepo
// （App JWT 调 /repos/{o}/{r}/installation）。未知安装 id 的调用方（rerun）
// 必须先解析再发请求，不能把 0 当安装 id 继续。
func (c *Client) ResolveInstallation(ctx context.Context, repoFullName string) (int64, error) {
	return c.store.InstallationForRepo(ctx, repoFullName)
}

// Bearer 暴露安装令牌给同进程的内部下载路径（私有仓库截图走 App 凭证）；
// 不是对外接口，凭证不出本进程。
func (c *Client) Bearer(ctx context.Context, installationID int64) (string, error) {
	return c.store.Bearer(ctx, installationID)
}

// 编译期钉住薄壳仍满足 seam 契约。
var _ githubapi.GitHubClient = (*Client)(nil)

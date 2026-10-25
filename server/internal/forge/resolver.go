package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// ReviewClientResolver 按 (集成, 仓库, 安装) 解析审查链路的访问面。审查侧的
// integrationID 是归一快照（空串 = 内置 GitHub，其余为 forge_integrations.id）。
type ReviewClientResolver interface {
	// ClientFor 内置 GitHub 返回包好安装 id 的适配器；Forgejo 集成经凭证来源
	// 现取 SSRF 安全 client + PAT 组装适配器（INT-167 任务 3）。
	ClientFor(ctx context.Context, integrationID, repoFullName string, installationID int64) (ReviewClient, error)
}

// GitHubApps 解析器取得部署级 GitHub 访问面的最小接口，与 service.PRGitHubClients
// 结构同形（两包互不引用，靠接口满足）。
type GitHubApps interface {
	ClientFor(ctx context.Context) (githubapi.GitHubClient, error)
}

// ForgejoSource 解析器取得 Forgejo 集成访问三件套的最小接口（service 侧实现，
// INT-167 任务 3）：按集成行现取 SSRF 安全 client、展开后的 API 基址与解密后
// 的机器人 PAT。集成不存在/kind 不符/解密失败都在实现里报明确错误。
type ForgejoSource interface {
	ForgejoClient(ctx context.Context, integrationID string) (httpClient *http.Client, apiBaseURL, token string, err error)
}

// ReviewClients ReviewClientResolver 的实现：内置 GitHub 分支 + Forgejo 分支。
// repoFullName 只进错误文案；Forgejo 适配器按集成凭证构造时也不按仓库分支。
type ReviewClients struct {
	gh      GitHubApps
	forgejo ForgejoSource
}

// 编译期钉住实现满足 resolver 契约。
var _ ReviewClientResolver = (*ReviewClients)(nil)

// NewReviewClients 用部署级 GitHub 访问面与 Forgejo 凭证来源构造 resolver；
// 两者均可为 nil（对应「访问面未配置」的既有口径，ClientFor 报明确错误）。
func NewReviewClients(gh GitHubApps, forgejo ForgejoSource) *ReviewClients {
	return &ReviewClients{gh: gh, forgejo: forgejo}
}

func (r *ReviewClients) ClientFor(ctx context.Context, integrationID, repoFullName string, installationID int64) (ReviewClient, error) {
	if integrationID != "" && integrationID != model.ForgeIntegrationBuiltinGitHubID {
		if r.forgejo == nil {
			return nil, fmt.Errorf("forge: 集成 %s（仓库 %s）的 Forgejo 凭证来源未配置", integrationID, repoFullName)
		}
		httpClient, apiBase, token, err := r.forgejo.ForgejoClient(ctx, integrationID)
		if err != nil {
			return nil, err
		}
		return NewForgejoReviewClient(httpClient, apiBase, token), nil
	}
	if r.gh == nil {
		return nil, errors.New("GitHub 访问面未配置")
	}
	api, err := r.gh.ClientFor(ctx)
	if err != nil {
		return nil, err
	}
	return NewGitHubReviewClient(api, installationID), nil
}

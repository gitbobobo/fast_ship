package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"gorm.io/gorm"
)

// 集成链接构造器（INT-166）：按代码仓集成 (kind, base_url) 生成 PR 链接与
// 文件行锚链接，供审查评论（review_sync）与记录/动态投影（review_records、
// review_activity）共用，替代此前四处硬编码的 github.com 拼接。
//
// URL 模板依据（Forgejo 是 Gitea 的社区分叉，路由一致，核实自 Gitea
// routers/web/web.go 与 templates/repo/view_file.tmpl）：
//   - PR 详情：/{owner}/{repo}/pulls/{index}
//   - 文件视图：/{owner}/{repo}/src/commit/{sha}/{path}（/blob/* 会被重定向
//     到 /src/commit/*，不是稳定形态）；行锚与 GitHub 同为 #L{n}
//     （view_file.tmpl: id="L{{$line}}"）。

// forgeLinks 一个代码仓集成的链接构造面：kind 决定 URL 惯例（github 的
// pull/blob vs forgejo 的 pulls/src/commit），baseURL 是该集成的实例基址。
type forgeLinks struct {
	kind    string
	baseURL string
	// unresolved 集成行解析失败（绑定的行已不存在 / 查询失败）：链接构造不
	// 出稳定目标，PullURL/BlobLineURL 返回空串而不是回落 github——私有
	// owner/repo 不能被拼到 github.com 上发出去。
	unresolved bool
}

// githubWebBase 本部署的 GitHub Web 基址：从 github_app.api_base_url 推导
// （api.github.com → github.com；GHE 的 https://<host>[:port]/api/v3 → 该
// host[:port]），与 review 附件的部署口径共用同一份推导。写死 github.com
// 会让 GHE 部署的审查链接与 code_repo_url 指向公共 GitHub——私有 owner/repo
// 落到错误主机，正是 forgejo 侧明确禁止的那类错误。
func githubWebBase(cfg *config.Config) string {
	return "https://" + githubmedia.GitHubWebHost(cfg.GitHubApp.APIBaseURL)
}

// githubLinks 内置 github-default 的构造面：base 是本部署的 GitHub Web 基址
// （见 githubWebBase），与部署口径一致，不再是写死的公共 github.com。
func githubLinks(base string) forgeLinks {
	return forgeLinks{kind: model.ForgeIntegrationKindGitHub, baseURL: base}
}

// forgeBase 集成基址去尾部 /（保存时已归一，这里防御存量 NULL 等历史形态）。
// 基址缺失一律空串、调用方按「链接不可渲染」处理——零值回落 github.com 会
// 在 GHE 部署下把链接静默指到公共主机，正是 forgejo 侧禁止的错误；生产路径
// 的 github 基址由 githubLinks(ghWebBase) 显式传入。
func (l forgeLinks) forgeBase() string {
	return strings.TrimRight(strings.TrimSpace(l.baseURL), "/")
}

// PullURL 一个 PR 的页面链接：github 为 {base}/{o}/{r}/pull/{n}，forgejo 为
// {base}/{o}/{r}/pulls/{n}。未知 kind 一律按 github 惯例（与「空列 = 内置
// github-default」的兜底口径一致）。
func (l forgeLinks) PullURL(repoFullName string, prNumber int) string {
	base := l.linkBase()
	if base == "" {
		return ""
	}
	suffix := "pull"
	if l.kind == model.ForgeIntegrationKindForgejo {
		suffix = "pulls"
	}
	return fmt.Sprintf("%s/%s/%s/%d", base, repoFullName, suffix, prNumber)
}

// linkBase 链接可构造的基址：unresolved 或基址解析为空（forgejo 行基址缺失）
// 返回空串，调用方按「链接不可用」处理，绝不退回 github.com。
func (l forgeLinks) linkBase() string {
	if l.unresolved {
		return ""
	}
	return l.forgeBase()
}

// BlobLineURL 指向 head 提交中文件行锚点的链接。文件路径按 `/` 分段
// PathEscape——路径里的括号/空格/`#` 不编码会断链（`#` 尤其会被当成锚点
// 截断）；repo/sha 只含合法字符，原样拼接。forgejo 为
// {base}/{o}/{r}/src/commit/{sha}/{path}#L{n}，其余按 github 的 blob 形态。
func (l forgeLinks) BlobLineURL(repoFullName, sha, path string, line int) string {
	base := l.linkBase()
	if base == "" {
		return ""
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	encoded := strings.Join(segments, "/")
	if l.kind == model.ForgeIntegrationKindForgejo {
		return fmt.Sprintf("%s/%s/src/commit/%s/%s#L%d",
			base, repoFullName, sha, encoded, line)
	}
	return fmt.Sprintf("%s/%s/blob/%s/%s#L%d",
		base, repoFullName, sha, encoded, line)
}

// forgeLinksForProject 单项目的链接构造面解析：读项目行的
// code_integration_id（空 = 内置 github-default），非空时查 forge_integrations
// 行取 (kind, base_url)。项目行查不到（含已删除与查询故障）、集成行已不存在
// （删除集成前应用层会拦引用，这里只防并发窗口）或任何查询失败时返回不可
// 解析标记，链接为空串——审查链接断掉不该让读路径失败，确认不了集成归属就
// 更不该把私有仓库地址发去 github.com。
func forgeLinksForProject(ctx context.Context, db *gorm.DB, projectID, ghBase string) forgeLinks {
	var project model.Project
	if err := db.WithContext(ctx).
		Select("id, code_integration_id").
		Where("id = ?", projectID).
		First(&project).Error; err != nil {
		return forgeLinks{unresolved: true}
	}
	return forgeLinksByProjects(ctx, db, []model.Project{project}, ghBase)[projectID]
}

// forgeLinksByProjects 批量解析项目 → 链接构造面：一次取被引用的
// forge_integrations 行。projects 由调用方加载（活动列表在同一事务里已取出
// 完整行），缺失或解析不了的条目兜底 github-default；ghBase 是本部署的
// GitHub Web 基址（githubWebBase）。
func forgeLinksByProjects(ctx context.Context, db *gorm.DB, projects []model.Project, ghBase string) map[string]forgeLinks {
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.CodeIntegrationID != "" {
			ids = append(ids, p.CodeIntegrationID)
		}
	}
	rows := map[string]model.ForgeIntegration{}
	if len(ids) > 0 {
		var list []model.ForgeIntegration
		if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&list).Error; err == nil {
			for _, row := range list {
				rows[row.ID] = row
			}
		}
		// 查询失败不向上传播：被引用行查不到时按 unresolved 处理（见下）。
	}
	out := make(map[string]forgeLinks, len(projects))
	for _, p := range projects {
		if p.CodeIntegrationID == "" {
			out[p.ID] = githubLinks(ghBase)
			continue
		}
		if row, ok := rows[p.CodeIntegrationID]; ok {
			out[p.ID] = forgeLinks{kind: row.Kind, baseURL: row.BaseURL}
			continue
		}
		// 绑定的集成行不在（删除拦挡是应用层，这里只防并发窗口）：标记不可
		// 解析，链接返回空串——不把私有 owner/repo 拼到 github.com 上。
		out[p.ID] = forgeLinks{unresolved: true}
	}
	return out
}

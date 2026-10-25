package service

import (
	"context"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 集成链接构造器（INT-166）：github 形态必须与旧硬编码逐字一致
// （https://github.com/{o}/{r}/pull/{n}、/blob/{sha}/{path}#L{n}），forgejo 按
// Gitea/Forgejo 路由惯例（/pulls/{n}、/src/commit/{sha}/{path}#L{n}，依据见
// forge_links.go 头注与 docs/internal/forge-integrations.md）。

func TestForgeLinksPullURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links forgeLinks
		want  string
	}{
		// 零值无基址：链接不可渲染（回落 github.com 会在 GHE 部署指错主机）。
		{"zero value has no base", forgeLinks{}, ""},
		{"builtin github", githubLinks("https://github.com"), "https://github.com/o/r/pull/7"},
		{"forgejo", forgeLinks{kind: model.ForgeIntegrationKindForgejo, baseURL: "https://git.example.com"}, "https://git.example.com/o/r/pulls/7"},
		{"forgejo base trailing slash", forgeLinks{kind: model.ForgeIntegrationKindForgejo, baseURL: "https://git.example.com/"}, "https://git.example.com/o/r/pulls/7"},
		{"unknown kind uses github convention", forgeLinks{kind: "mystery", baseURL: "https://x.example.com"}, "https://x.example.com/o/r/pull/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.links.PullURL("o/r", 7); got != tc.want {
				t.Fatalf("PullURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestForgeLinksBlobLineURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links forgeLinks
		file  string
		line  int
		want  string
	}{
		// 零值无基址：链接不可渲染（与 PullURL 同口径）。
		{"zero value has no base", forgeLinks{}, "internal/service/project.go", 12, ""},
		{"builtin github", githubLinks("https://github.com"), "main.go", 1,
			"https://github.com/o/r/blob/abc123/main.go#L1"},
		{"path segments escaped (spaces/parens/hash)", githubLinks("https://github.com"), "docs/a b (v2)/file#1.go", 3,
			"https://github.com/o/r/blob/abc123/docs/a%20b%20%28v2%29/file%231.go#L3"},
		{"forgejo src/commit", forgeLinks{kind: model.ForgeIntegrationKindForgejo, baseURL: "https://git.example.com"}, "pkg/x/y.go", 42,
			"https://git.example.com/o/r/src/commit/abc123/pkg/x/y.go#L42"},
		{"forgejo escapes too", forgeLinks{kind: model.ForgeIntegrationKindForgejo, baseURL: "https://git.example.com"}, "a b.go", 5,
			"https://git.example.com/o/r/src/commit/abc123/a%20b.go#L5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.links.BlobLineURL("o/r", "abc123", tc.file, tc.line); got != tc.want {
				t.Fatalf("BlobLineURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// 解析器兜底（INT-166）：未绑定集成（空列）→ 兜底 github-default；项目行
// 查不到（含已删除与查询故障）、绑定的集成行已被删（删除前应用层会拦引用，
// 这里只防并发窗口）→ 不可解析、链接为空串；命中 forgejo 集成 → forgejo
// 形态。
func TestForgeLinksForProject(t *testing.T) {
	svc := setupTestServices(t)
	ctx := context.Background()
	user := createTestUser(t, svc.db, "user-forge-links")
	forge := createTestForgeIntegration(t, svc.db, "https://git.example.com")

	// 项目不存在 → 不可解析：确认不了集成归属就不猜 github，链接为空串。
	if links := forgeLinksForProject(ctx, svc.db, "no-such-project", "https://github.com"); links.PullURL("o/r", 1) != "" {
		t.Fatalf("missing project should yield empty pr url, got %q", links.PullURL("o/r", 1))
	}

	// 空列（= 内置 github-default）→ github。
	bare := createTestProject(t, svc.db, user.ID)
	links := forgeLinksForProject(ctx, svc.db, bare.ID, "https://github.com")
	if links.PullURL("o/r", 1) != "https://github.com/o/r/pull/1" {
		t.Fatalf("empty binding should fall back to github, got %q", links.PullURL("o/r", 1))
	}

	// 命中 forgejo 集成 → forgejo 形态。
	bound := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.CodeIntegrationID = forge.ID
	})
	links = forgeLinksForProject(ctx, svc.db, bound.ID, "https://github.com")
	if got := links.PullURL("o/r", 2); got != "https://git.example.com/o/r/pulls/2" {
		t.Fatalf("forgejo bound project PullURL = %q", got)
	}

	// 集成行已删 → 链接不可解析，返回空串（防御性；删除集成前有引用拦挡）。
	// 不回落 github：私有 owner/repo 不能被拼到 github.com 上发出去。
	if err := svc.db.Where("id = ?", forge.ID).Delete(&model.ForgeIntegration{}).Error; err != nil {
		t.Fatalf("delete forge integration: %v", err)
	}
	links = forgeLinksForProject(ctx, svc.db, bound.ID, "https://github.com")
	if got := links.PullURL("o/r", 2); got != "" {
		t.Fatalf("deleted integration should yield empty pr url, got %q", got)
	}
	if got := links.BlobLineURL("o/r", "abc123", "main.go", 2); got != "" {
		t.Fatalf("deleted integration should yield empty blob url, got %q", got)
	}
}

// 批量解析：forgejo 绑定与空列项目混合时各归各位（活动列表一条查询取回全部
// 被引用集成）。
func TestForgeLinksByProjects(t *testing.T) {
	svc := setupTestServices(t)
	ctx := context.Background()
	user := createTestUser(t, svc.db, "user-forge-links-batch")
	forge := createTestForgeIntegration(t, svc.db, "https://forge.batch.org")

	bound := createTestProject(t, svc.db, user.ID, func(p *model.Project) { p.CodeIntegrationID = forge.ID })
	bare := createTestProject(t, svc.db, user.ID)

	projects := []model.Project{*bound, *bare}
	linksByProject := forgeLinksByProjects(ctx, svc.db, projects, "https://github.com")
	if got := linksByProject[bound.ID].PullURL("o/r", 9); got != "https://forge.batch.org/o/r/pulls/9" {
		t.Fatalf("bound project = %q", got)
	}
	if got := linksByProject[bare.ID].PullURL("o/r", 9); got != "https://github.com/o/r/pull/9" {
		t.Fatalf("bare project = %q", got)
	}
	// 未知项目条目兜底 github（map 缺键不 panic 的口径由调用方保证，这里只
	// 验证存在的键解析正确、总数不多不少）。
	if len(linksByProject) != 2 {
		t.Fatalf("linksByProject size = %d, want 2", len(linksByProject))
	}
}

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// INT-166 审查修复的定向测试：同归一主机唯一、base_url 站点根收口、集成身份
// 变更停审查、Forgejo 项目挡在 GitHub 审查链路之外、code_repo_url 投影
// （集成行缺失时 null，不回落 github.com）。

func TestForgeIntegrationHostUniqueness(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	first, err := env.svc.Create(ctx, forgeCreateReq("a", "https://forge.example.com", "tok"))
	if err != nil {
		t.Fatalf("create a: %v", err)
	}

	// 同一归一主机（端口按 scheme 缺省归一）重复 → 40001 且点名占用集成。
	_, err = env.svc.Create(ctx, forgeCreateReq("b", "https://forge.example.com:443/", "tok"))
	dup := appErr(t, err)
	if dup.Code != 40001 || !strings.Contains(dup.Message, "a") {
		t.Fatalf("dup create = %d %q, want 40001 mentioning 集成 a", dup.Code, dup.Message)
	}
	if rows, _ := env.repo.List(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1（重复建入未落库）", len(rows))
	}

	// 不同端口 / 不同 scheme 缺省端口视为不同主机，可各建一条。
	if _, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "c", BaseUrl: "https://forge.example.com:8443", Token: "tok",
	}); err != nil {
		t.Fatalf("create c (different port): %v", err)
	}
	second, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "d", BaseUrl: "https://other.example.com", Token: "tok",
	})
	if err != nil {
		t.Fatalf("create d: %v", err)
	}

	// Update 撞其他集成 → 40001；改 api_base_url 不触发查重。
	if _, err := env.svc.Update(ctx, first.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://forge.example.com:8443"),
	}); appErr(t, err).Code != 40001 {
		t.Fatalf("update collide code = %d, want 40001", appErr(t, err).Code)
	}
	if _, err := env.svc.Update(ctx, second.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr("https://other.example.com/api/v2"),
	}); err != nil {
		t.Fatalf("update api base should not trigger host check: %v", err)
	}
}

// 同归一主机的数据库唯一索引兜底（审查修复）：应用层查重（ensureHostAvailable）
// 通过之后同主机被并发抢注的窗口由 idx_forge_integrations_host_key 关掉，唯一
// 索引冲突折回同类 40001；host_key 随 base_url 落库与更新。
func TestForgeIntegrationHostKeyDBBackstop(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()
	// 测试库 AutoMigrate 不建手工索引，按生产 createBootIndexes 的 DDL 补上。
	if err := env.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_forge_integrations_host_key ON forge_integrations(host_key) WHERE host_key <> ''").Error; err != nil {
		t.Fatalf("create index: %v", err)
	}

	// 竞态对手方的确定性替身：base_url 脏行（算不出归一键，应用层查重看不见
	// 它），host_key 却已占住 forge.example.com:443。
	rival := &model.ForgeIntegration{
		ID:        uuid.NewString(),
		Kind:      model.ForgeIntegrationKindForgejo,
		Name:      "rival",
		HostKey:   "forge.example.com:443",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := env.db.Create(rival).Error; err != nil {
		t.Fatalf("seed rival: %v", err)
	}

	// 应用层查重放行（rival 的 base_url 算不出键），落库撞唯一索引 →
	// hostTakenErr 按 host_key 列点查，占用行的 base_url 虽脏但 host_key 是
	// 占键的那一份，照样能点名占用者。
	_, err := env.svc.Create(ctx, forgeCreateReq("b", "https://forge.example.com", "tok"))
	taken := appErr(t, err)
	if taken.Code != errs.ErrInvalidParams.Code || !strings.Contains(taken.Message, "rival") {
		t.Fatalf("backstop create = %d %q, want 40001 naming rival", taken.Code, taken.Message)
	}
	if rows, _ := env.repo.List(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1（撞索引的创建未落库）", len(rows))
	}

	// 正常创建的行带归一键；hostTakenErr 按键现查点名占用集成。
	named, err := env.svc.Create(ctx, forgeCreateReq("a", "https://named.example.com", "tok"))
	if err != nil {
		t.Fatalf("create named: %v", err)
	}
	row, err := env.repo.FindByID(named.Id)
	if err != nil {
		t.Fatalf("find named: %v", err)
	}
	if row.HostKey != "named.example.com:443" {
		t.Fatalf("stored host_key = %q, want named.example.com:443", row.HostKey)
	}
	mention := env.svc.hostTakenErr("named.example.com:443")
	if mention.Code != errs.ErrInvalidParams.Code || !strings.Contains(mention.Message, "a") {
		t.Fatalf("hostTakenErr = %d %q, want 40001 mentioning a", mention.Code, mention.Message)
	}

	// Update 换基址同步改写 host_key。
	if _, err := env.svc.Update(ctx, named.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://named.example.com:8443"),
	}); err != nil {
		t.Fatalf("update base: %v", err)
	}
	row, err = env.repo.FindByID(named.Id)
	if err != nil {
		t.Fatalf("find named 2: %v", err)
	}
	if row.HostKey != "named.example.com:8443" {
		t.Fatalf("updated host_key = %q, want named.example.com:8443", row.HostKey)
	}
}

func TestForgeIntegrationBaseRootRequired(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	// base_url 带路径拒绝：审查链接与 code_repo_url 都按 {base}/{o}/{r} 拼接。
	_, err := env.svc.Create(ctx, forgeCreateReq("n", "https://host.example.com/forgejo", "tok"))
	bad := appErr(t, err)
	if bad.Code != 40001 || !strings.Contains(bad.Message, "站点根") {
		t.Fatalf("subpath base = %d %q, want 40001 站点根", bad.Code, bad.Message)
	}

	// api_base_url 允许带路径（/api/v1 惯例）。
	if _, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name:       "n2",
		BaseUrl:    "https://host2.example.com",
		ApiBaseUrl: ptrStr("https://host2.example.com/forgejo/api/v1"),
		Token:      "tok",
	}); err != nil {
		t.Fatalf("api base with path should pass: %v", err)
	}

	// Update 换成带路径 base 同样拒绝，且行保持原值。
	created, err := env.svc.Create(ctx, forgeCreateReq("n3", "https://host3.example.com", "tok"))
	if err != nil {
		t.Fatalf("create n3: %v", err)
	}
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://host3.example.com/git/"),
	}); appErr(t, err).Code != 40001 {
		t.Fatalf("update to subpath code = %d, want 40001", appErr(t, err).Code)
	}
	row, _ := env.repo.FindByID(created.Id)
	if row.BaseURL != "https://host3.example.com" {
		t.Fatalf("failed update must not persist base, got %q", row.BaseURL)
	}
}

func TestProjectRepoUpdate_IntegrationChangeStopsReview(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-integration-review-sync")
	forge1 := createTestForgeIntegration(t, svc.db, "https://git1.example.com")
	forge2 := createTestForgeIntegration(t, svc.db, "https://git2.example.com")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "acme"
		p.GithubRepo = "app"
		p.CodeIntegrationID = forge1.ID
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", Enabled: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	// owner/repo 字面不变、只换集成身份：等于换了审查仓库，审查必须停用。
	project.CodeIntegrationID = forge2.ID
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project: %v", err)
	}
	var got model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", project.ID).First(&got).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if got.Enabled {
		t.Fatal("integration 变更后审查设置应停用")
	}

	// 集成不变的无关更新不触发停用。
	if err := svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).Update("enabled", true).Error; err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project 2: %v", err)
	}
	if err := svc.db.Where("project_id = ?", project.ID).First(&got).Error; err != nil {
		t.Fatalf("load settings 2: %v", err)
	}
	if !got.Enabled {
		t.Fatal("集成不变的更新不应停用审查")
	}
}

func TestUpdateSettings_ForgejoRepoRejected(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-forge-review-gate")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "acme"
		p.GithubRepo = "app"
		p.CodeIntegrationID = "forge-any"
	})
	review, _ := readyReviewService(t, svc)

	// Forgejo 代码仓不进 GitHub 审查链路：开启前显式拒绝，不落必败设置。
	_, err := review.UpdateSettings(context.Background(), user.ID, project.ID,
		&api.ReviewSettingsInput{Enabled: true})
	gate := appErr(t, err)
	if gate.Code != 40001 || !strings.Contains(gate.Message, "后续版本") {
		t.Fatalf("enable forgejo review = %d %q, want 40001 后续版本", gate.Code, gate.Message)
	}
	var rows int64
	svc.db.Model(&model.ProjectReviewSettings{}).Where("project_id = ?", project.ID).Count(&rows)
	if rows != 0 {
		t.Fatalf("rejected enable must not persist settings, rows = %d", rows)
	}
}

func TestFindEnabledReviewProjectSkipsForgejoBound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-forge-webhook-gate")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.CodeIntegrationID = "forge-x"
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", Enabled: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	review := newReviewTestService(t, svc, nil, nil, nil)

	// 绑定 Forgejo 的项目不进 GitHub webhook 审查链路：按未命中处理。
	got, settings, err := review.findEnabledReviewProjectTx(svc.db, "acme/app")
	if err != nil || got != nil || settings != nil {
		t.Fatalf("forgejo-bound project should not match, got %+v %+v %v", got, settings, err)
	}

	// 对照：解除绑定后同仓库命中。
	if err := svc.db.Model(&model.Project{}).Where("id = ?", project.ID).
		Update("code_integration_id", "").Error; err != nil {
		t.Fatalf("clear binding: %v", err)
	}
	got, settings, err = review.findEnabledReviewProjectTx(svc.db, "acme/app")
	if err != nil || got == nil || settings == nil {
		t.Fatalf("unbound project should match, got %+v %+v %v", got, settings, err)
	}
}

func TestProjectCodeRepoUrl(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-coderepourl")
	forge := createTestForgeIntegration(t, svc.db, "https://git.example.com")
	psvc := newTestProjectService(svc)

	// 内置 github：https://github.com/{o}/{r}。
	gh, err := psvc.Create(user.ID, &CreateProjectRequest{
		Name: "gh", RepositoryUrl: api.Ptr("owner/repo"),
	})
	if err != nil {
		t.Fatalf("create gh: %v", err)
	}
	if gh.CodeRepoUrl == nil || *gh.CodeRepoUrl != "https://github.com/owner/repo" {
		t.Fatalf("github code_repo_url = %v", gh.CodeRepoUrl)
	}

	// forgejo 集成：{base_url}/{o}/{r}。
	fj, err := psvc.Create(user.ID, &CreateProjectRequest{
		Name:              "fj",
		CodeRepositoryUrl: api.Ptr("https://git.example.com/acme/app"),
	})
	if err != nil {
		t.Fatalf("create fj: %v", err)
	}
	if fj.CodeRepoUrl == nil || *fj.CodeRepoUrl != "https://git.example.com/acme/app" {
		t.Fatalf("forgejo code_repo_url = %v", fj.CodeRepoUrl)
	}

	// 集成行删除 → null（绝不回落 github.com 把私有 o/r 发出去）。
	if err := svc.db.Where("id = ?", forge.ID).Delete(&model.ForgeIntegration{}).Error; err != nil {
		t.Fatalf("delete forge: %v", err)
	}
	got, err := psvc.Get(fj.Id, user.ID)
	if err != nil {
		t.Fatalf("get fj: %v", err)
	}
	if got.CodeRepoUrl != nil {
		t.Fatalf("missing integration should yield null, got %q", *got.CodeRepoUrl)
	}

	// List 路径同样成立（批量预取，缺失行降级 null）。
	list, _, err := psvc.List(user.ID, 1, 10, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, item := range list {
		if item.Id == fj.Id && item.CodeRepoUrl != nil {
			t.Fatalf("list item fj code_repo_url = %q, want null", *item.CodeRepoUrl)
		}
		if item.Id == gh.Id && (item.CodeRepoUrl == nil || *item.CodeRepoUrl != "https://github.com/owner/repo") {
			t.Fatalf("list item gh code_repo_url = %v", item.CodeRepoUrl)
		}
	}

	// 无代码仓（owner/repo 缺一）→ null。
	bare, err := psvc.Create(user.ID, &CreateProjectRequest{Name: "bare"})
	if err != nil {
		t.Fatalf("create bare: %v", err)
	}
	if bare.CodeRepoUrl != nil {
		t.Fatalf("no-repo project code_repo_url = %q, want null", *bare.CodeRepoUrl)
	}
}

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// fakeReviewAppSource github_app 前置条件的可控桩：ok 表示部署已配置 App 身份。
type fakeReviewAppSource struct {
	ok  bool
	err error
}

func (f fakeReviewAppSource) Current(context.Context) (GitHubAppConfig, bool, error) {
	return GitHubAppConfig{}, f.ok, f.err
}

// fakeReviewPublicURL public_url 前置条件的可控桩。
type fakeReviewPublicURL string

func (f fakeReviewPublicURL) PublicURL(context.Context) string { return string(f) }

// newReviewTestService 组装一个仅服务于设置/记录查询的 ReviewService；
// 调度循环不启动（webhook 派发面不在测试范围）。
func newReviewTestService(t *testing.T, svc *testServices, gh PRGitHubClients, app GitHubAppSource, url PublicURLSource) *ReviewService {
	t.Helper()
	return NewReviewService(svc.db, svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(svc.db),
		NewNotificationSink(svc.db), gh, app, url, nil, nil, nil, repository.NewForgeIntegrationRepository(svc.db), svc.cfg, zap.NewNop())
}

// readyReviewService 所有前置条件就绪的 ReviewService：App 已配置、安装可达、
// public_url 已配；项目 Token 由 createTestProject 默认绑定。
func readyReviewService(t *testing.T, svc *testServices) (*ReviewService, *githubapp.Fake) {
	fake := githubapp.NewFake()
	return newReviewTestService(t, svc,
		githubapp.StaticClients{Client: fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com")), fake
}

// createTestT3Binding 落一条项目↔环境绑定（t3_project_id 为快照真值来源）。
func createTestT3Binding(t *testing.T, db *gorm.DB, projectID, environmentID, t3ProjectID string) *model.ProjectT3Binding {
	t.Helper()
	now := time.Now().UTC()
	b := &model.ProjectT3Binding{
		ProjectID:      projectID,
		EnvironmentID:  environmentID,
		T3ProjectID:    t3ProjectID,
		T3ProjectTitle: "t3-proj",
		WorkspaceRoot:  "/ws",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	return b
}

// testCombo 一个合法组合（指定环境）。
func testCombo(envID string) api.ReviewCombination {
	return api.ReviewCombination{
		EnvironmentId: envID,
		InstanceId:    "claude",
		Model:         "claude-sonnet-4",
		Options:       []api.ReviewModelOption{{Id: "effort", Value: "medium"}},
	}
}

func validSettingsInput(envID string) *api.ReviewSettingsInput {
	return &api.ReviewSettingsInput{
		Enabled:               true,
		TriggerOpened:         true,
		TriggerSynchronize:    true,
		TriggerReadyForReview: false,
		TriggerReopened:       false,
		CodeCombinations:      []api.ReviewCombination{testCombo(envID)},
		VisualCombinations:    []api.ReviewCombination{},
	}
}

func appErrorCode(t *testing.T, err error) int {
	t.Helper()
	var appErr *errs.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("err = %v, want *errs.AppError", err)
	}
	return appErr.Code
}

// 前置条件缺失（public_url 未配 + 项目未绑 Token）→ 41202 且逐条列出缺项。
func TestUpdateSettings_MissingPrerequisites_Returns41202(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})
	env := createT3TestEnv(t, svc.db, user.ID, "env1")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-1")

	// public_url 空 → public_url 缺项；项目未绑 Token → github_token 缺项。
	rs, _ := readyReviewService(t, svc)
	rs = newReviewTestService(t, svc,
		githubapp.StaticClients{Client: githubapp.NewFake()},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL(""))

	err := func() error {
		_, e := rs.UpdateSettings(context.Background(), user.ID, project.ID, validSettingsInput(env.ID))
		return e
	}()
	if code := appErrorCode(t, err); code != 41202 {
		t.Fatalf("code = %d, want 41202 (err=%v)", code, err)
	}
	var appErr *errs.AppError
	errors.As(err, &appErr)
	for _, want := range []string{"public_url", "Token"} {
		if !strings.Contains(appErr.Message, want) {
			t.Fatalf("message 缺 %q: %s", want, appErr.Message)
		}
	}
}

// 组合数量/重复/环境归属/环境绑定 → 400xx。
func TestUpdateSettings_InvalidCombinations_Returns400(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	other := createTestUser(t, svc.db, "u2")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env1")
	env2 := createT3TestEnv(t, svc.db, user.ID, "env2")
	readOnlyEnv := createT3TestEnv(t, svc.db, user.ID, "read-only-env")
	if err := svc.db.Model(&model.T3Environment{}).Where("id = ?", readOnlyEnv.ID).
		Update("bearer_scope", "orchestration:read").Error; err != nil {
		t.Fatalf("set read-only scope: %v", err)
	}
	createTestT3Binding(t, svc.db, project.ID, readOnlyEnv.ID, "t3-ro")
	otherEnv := createT3TestEnv(t, svc.db, other.ID, "other-env")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-1")

	rs, _ := readyReviewService(t, svc)
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*api.ReviewSettingsInput)
	}{
		{name: "代码组合为 0", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = nil
		}},
		{name: "代码组合超 5", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{
				testCombo(env.ID), testCombo(env.ID), testCombo(env.ID),
				testCombo(env.ID), testCombo(env.ID), testCombo(env.ID),
			}
		}},
		{name: "视觉组合超 3", mutate: func(in *api.ReviewSettingsInput) {
			in.VisualCombinations = []api.ReviewCombination{
				testCombo(env.ID), testCombo(env.ID), testCombo(env.ID), testCombo(env.ID),
			}
		}},
		{name: "组合重复", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{testCombo(env.ID), testCombo(env.ID)}
		}},
		{name: "环境属于其他用户", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{testCombo(otherEnv.ID)}
		}},
		{name: "环境未绑定项目", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{testCombo(env2.ID)}
		}},
		{name: "环境不存在", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{testCombo(uuid.NewString())}
		}},
		{name: "环境是只读会话", mutate: func(in *api.ReviewSettingsInput) {
			in.CodeCombinations = []api.ReviewCombination{testCombo(readOnlyEnv.ID)}
		}},
		{name: "组合缺 model", mutate: func(in *api.ReviewSettingsInput) {
			c := testCombo(env.ID)
			c.Model = ""
			in.CodeCombinations = []api.ReviewCombination{c}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validSettingsInput(env.ID)
			tc.mutate(in)
			_, err := rs.UpdateSettings(ctx, user.ID, project.ID, in)
			code := appErrorCode(t, err)
			if code < 40000 || code > 40999 {
				t.Fatalf("code = %d, want 400xx (err=%v)", code, err)
			}
		})
	}
}

// 同仓库被其他用户的项目启用 → 40922（消息含占用者登录名）；自己重复保存不冲突。
func TestUpdateSettings_RepoTaken_Returns40922(t *testing.T) {
	svc := setupTestServices(t)
	owner := createTestUser(t, svc.db, "owner")
	other := createTestUser(t, svc.db, "other")

	p1 := createTestProject(t, svc.db, owner.ID, func(p *model.Project) {
		p.GithubOwner = "OctoCat"
		p.GithubRepo = "Hello"
	})
	env1 := createT3TestEnv(t, svc.db, owner.ID, "e1")
	createTestT3Binding(t, svc.db, p1.ID, env1.ID, "t3-1")

	rs, _ := readyReviewService(t, svc)
	ctx := context.Background()

	if _, err := rs.UpdateSettings(ctx, owner.ID, p1.ID, validSettingsInput(env1.ID)); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	// 自己重复保存：不触发 40922。
	if _, err := rs.UpdateSettings(ctx, owner.ID, p1.ID, validSettingsInput(env1.ID)); err != nil {
		t.Fatalf("re-save own settings: %v", err)
	}

	// 另一用户同仓库（大小写不同）启用 → 40922。
	p2 := createTestProject(t, svc.db, other.ID, func(p *model.Project) {
		p.GithubOwner = "octocat"
		p.GithubRepo = "hello"
	})
	env2 := createT3TestEnv(t, svc.db, other.ID, "e2")
	createTestT3Binding(t, svc.db, p2.ID, env2.ID, "t3-2")

	_, err := rs.UpdateSettings(ctx, other.ID, p2.ID, validSettingsInput(env2.ID))
	if code := appErrorCode(t, err); code != 40922 {
		t.Fatalf("code = %d, want 40922 (err=%v)", code, err)
	}
	var appErr *errs.AppError
	errors.As(err, &appErr)
	if !strings.Contains(appErr.Message, "user_owner") {
		t.Fatalf("message 应含占用者登录名 user_owner: %s", appErr.Message)
	}
}

// 启用保存后读回一致；t3_project_id 从绑定快照而非客户端输入。
func TestUpdateSettings_SaveAndRead_Consistent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env1")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-snapshot-1")
	env2 := createT3TestEnv(t, svc.db, user.ID, "env2")
	createTestT3Binding(t, svc.db, project.ID, env2.ID, "t3-snapshot-2")

	rs, _ := readyReviewService(t, svc)
	ctx := context.Background()

	in := validSettingsInput(env.ID)
	in.TriggerReadyForReview = true
	visual := testCombo(env2.ID)
	visual.InstanceId = "codex"
	visual.Model = "gpt-5"
	in.VisualCombinations = []api.ReviewCombination{visual}
	// 客户端回传的伪造值应被绑定表真值覆盖。
	in.CodeCombinations[0].T3ProjectId = api.Ptr("forged")

	got, err := rs.UpdateSettings(ctx, user.ID, project.ID, in)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !got.Enabled || !got.TriggerReadyForReview {
		t.Fatalf("enabled/triggers 未保存: %+v", got)
	}
	if len(got.CodeCombinations) != 1 || len(got.VisualCombinations) != 1 {
		t.Fatalf("combos = %+v/%+v", got.CodeCombinations, got.VisualCombinations)
	}
	if got.CodeCombinations[0].T3ProjectId == nil || *got.CodeCombinations[0].T3ProjectId != "t3-snapshot-1" {
		t.Fatalf("t3_project_id 快照错误: %+v", got.CodeCombinations[0].T3ProjectId)
	}
	if got.VisualCombinations[0].T3ProjectId == nil || *got.VisualCombinations[0].T3ProjectId != "t3-snapshot-2" {
		t.Fatalf("visual t3_project_id 快照错误: %+v", got.VisualCombinations[0].T3ProjectId)
	}
	if got.CodeCombinations[0].EnvironmentName == nil || *got.CodeCombinations[0].EnvironmentName != "env1" {
		t.Fatalf("environment_name 未回填: %+v", got.CodeCombinations[0].EnvironmentName)
	}
	if got.CanManage == nil || !*got.CanManage || got.TakenBy != nil {
		t.Fatalf("can_manage/taken_by = %v/%v", got.CanManage, got.TakenBy)
	}

	// 读回：与保存响应一致（再比较触发项）。
	back, err := rs.GetSettings(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !back.Enabled || back.TriggerReadyForReview != got.TriggerReadyForReview ||
		*back.CodeCombinations[0].T3ProjectId != "t3-snapshot-1" {
		t.Fatalf("读回不一致: %+v", back)
	}
	for _, p := range back.Prerequisites {
		if p.Missing {
			t.Fatalf("前置条件应全部满足: %+v", p)
		}
	}

	// 未关联仓库的项目 repo_full_name 不回显 "/"（CodeRepoFullName 双空拼出
	// "/"），响应给空串。
	bare := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})
	bareSettings, err := rs.GetSettings(ctx, user.ID, bare.ID)
	if err != nil {
		t.Fatalf("GetSettings bare: %v", err)
	}
	if bareSettings.RepoFullName != "" {
		t.Fatalf("repo_full_name = %q, want 空串（未关联仓库）", bareSettings.RepoFullName)
	}

	// 关闭允许零组合。
	off, err := rs.UpdateSettings(ctx, user.ID, project.ID, &api.ReviewSettingsInput{
		Enabled: false, CodeCombinations: nil, VisualCombinations: nil,
	})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if off.Enabled {
		t.Fatalf("enabled 应为 false")
	}

	// 其他用户的项目 → 40401。
	if _, err := rs.GetSettings(ctx, user.ID, uuid.NewString()); appErrorCode(t, err) != 40401 {
		t.Fatalf("other project code = %d, want 40401", appErrorCode(t, err))
	}
}

// 部署未配置 App 时 GetSettings 的 prerequisites 标出缺项，且不发起仓库调用。
func TestGetSettings_Prerequisites_MissingApp(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)

	fake := githubapp.NewFake()
	rs := newReviewTestService(t, svc,
		githubapp.StaticClients{Client: fake},
		fakeReviewAppSource{ok: false},
		fakeReviewPublicURL("https://fs.example.com"))

	got, err := rs.GetSettings(context.Background(), user.ID, project.ID)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	byCode := map[api.ReviewPrerequisiteCode]api.ReviewPrerequisite{}
	for _, p := range got.Prerequisites {
		byCode[p.Code] = p
	}
	if !byCode[api.GithubApp].Missing {
		t.Fatalf("github_app 应缺失")
	}
	if !byCode[api.GithubAppInstallation].Missing {
		t.Fatalf("github_app_installation 应缺失")
	}
	if byCode[api.PublicUrl].Missing || byCode[api.GithubToken].Missing {
		t.Fatalf("public_url/github_token 不应缺失: %+v", byCode)
	}
	// 未配置 App 时安装校验不应发起 GitHub 调用。
	if fake.CallCounts()["GetRepository"] != 0 {
		t.Fatalf("未配置 App 不应调用 GetRepository: %+v", fake.CallCounts())
	}
}

// 停用审查时取消在途/排队运行并清防抖：调度只按 status 挑选，留着会继续
// 开线程。
func TestUpdateSettingsDisableCancelsRuns(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "disable-cancel")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env-disable")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-1")
	now := time.Now().UTC()

	queued := &model.ReviewRun{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            uuid.NewString(), UserID: user.ID, ProjectID: project.ID,
		Kind: model.ReviewKindCode, RepoFullName: "o/r", PRNumber: 3, HeadSHA: "sha",
		RoundNumber: 1, EnvironmentID: env.ID, InstanceID: "i", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: model.ReviewRunQueued, DeadlineAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(queued).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := svc.db.Create(&model.ReviewRunToken{
		RunID: queued.ID, TokenHash: "hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if err := svc.db.Create(&model.ReviewDebounce{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     project.ID, RepoFullName: "o/r", PRNumber: 3, HeadSHA: "sha2",
		Action: "synchronize", FireAt: now.Add(time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}

	rs, _ := readyReviewService(t, svc)
	input := validSettingsInput(env.ID)
	input.Enabled = false
	if _, err := rs.UpdateSettings(context.Background(), user.ID, project.ID, input); err != nil {
		t.Fatalf("disable: %v", err)
	}

	var run model.ReviewRun
	if err := svc.db.First(&run, "id = ?", queued.ID).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.Status != model.ReviewRunCancelled || run.ErrorCode != "disabled" {
		t.Fatalf("run = %s/%s, want cancelled/disabled", run.Status, run.ErrorCode)
	}
	var token model.ReviewRunToken
	svc.db.First(&token, "run_id = ?", queued.ID)
	if token.ConsumedAt == nil {
		t.Fatal("停用后运行令牌应已消费")
	}
	var debounces int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&debounces)
	if debounces != 0 {
		t.Fatalf("debounce rows = %d, want 0", debounces)
	}
}

// forgejoBoundClient 包装一层 GitHub 客户端：前置条件的 GetRepository 一过就
// 把项目改绑到 forgejo 集成——模拟「入参快照之后、写事务之前」的并发改绑；
// 写事务的集成身份复核必须拦下这次启用。
type forgejoBoundClient struct {
	githubapi.GitHubClient
	db        *gorm.DB
	projectID string
	done      bool
}

func (c *forgejoBoundClient) GetRepository(ctx context.Context, inst int64, repo string) error {
	if !c.done {
		c.done = true
		if err := c.db.Model(&model.Project{}).Where("id = ?", c.projectID).
			Update("code_integration_id", "forge-concurrent").Error; err != nil {
			return err
		}
	}
	return c.GitHubClient.GetRepository(ctx, inst, repo)
}

// TestGetSettings_ForgejoBinding_SkipsGitHubChecks C：Forgejo 绑定项目的
// GetSettings 不向 GitHub 发请求——仓库名不该拿去查 App 安装，同名 GitHub
// 仓已安装也不能误报为可用。两项 GitHub 前置条件按「不支持」口径缺项并
// 点明 Forgejo，其余前置条件照常计算。
func TestGetSettings_ForgejoBinding_SkipsGitHubChecks(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "forgejo-u")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "octocat" // 同名 GitHub 仓的安装状态与 Forgejo 仓无关
		p.GithubRepo = "hello"
		p.CodeIntegrationID = "forge-any"
	})

	fake := githubapp.NewFake()
	rs := newReviewTestService(t, svc,
		githubapp.StaticClients{Client: fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"))

	got, err := rs.GetSettings(context.Background(), user.ID, project.ID)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if n := fake.CallCounts()["GetRepository"]; n != 0 {
		t.Fatalf("GetRepository 调了 %d 次，want 0（Forgejo 仓库名不发 GitHub）", n)
	}
	byCode := map[api.ReviewPrerequisiteCode]api.ReviewPrerequisite{}
	for _, p := range got.Prerequisites {
		byCode[p.Code] = p
	}
	for _, code := range []api.ReviewPrerequisiteCode{api.GithubApp, api.GithubAppInstallation} {
		p, ok := byCode[code]
		if !ok {
			t.Fatalf("缺 %s 前置项", code)
		}
		if !p.Missing || p.Detail == nil || !strings.Contains(*p.Detail, "Forgejo") {
			t.Fatalf("%s = %+v，want 缺项且点明 Forgejo 不支持", code, p)
		}
	}
	if byCode[api.PublicUrl].Missing || byCode[api.GithubToken].Missing {
		t.Fatalf("其余前置条件不应缺项: %+v", byCode)
	}
}

// TestUpdateSettings_ForgejoBoundInTx_Rejected D：入参侧快照（事务外）还没
// 有 forgejo 绑定、前置校验与写事务之间项目被并发改绑 → 启用保存 40001 且
// 设置不落库。INT-167 任务 3 起 forgejo 可启用，事务内复核改为校验「绑定的
// 集成行存在且 kind=forgejo」：并发改绑指向不存在的集成报「集成已失效」，
// 快照与库内身份不一致由既有的「仓库已变更」复核兜住。
func TestUpdateSettings_ForgejoBoundInTx_Rejected(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env1")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-1")

	fake := githubapp.NewFake()
	rs := newReviewTestService(t, svc,
		githubapp.StaticClients{Client: &forgejoBoundClient{
			GitHubClient: fake, db: svc.db, projectID: project.ID,
		}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"))

	_, err := rs.UpdateSettings(context.Background(), user.ID, project.ID,
		validSettingsInput(env.ID))
	if code := appErrorCode(t, err); code != 40001 {
		t.Fatalf("code = %d, want 40001 (err=%v)", code, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "集成已失效") && !strings.Contains(msg, "仓库已变更") {
		t.Fatalf("message = %q, want 点明集成失效或仓库已变更", msg)
	}
	var n int64
	svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatal("集成身份复核拒绝时设置不得落库")
	}
}

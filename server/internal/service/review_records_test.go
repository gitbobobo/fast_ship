package service

import (
	"context"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// createTestReviewPR 落一条审查总账行。
func createTestReviewPR(t *testing.T, db *gorm.DB, userID, projectID, repo string, prNumber, round int, opts ...func(*model.ReviewPullRequest)) *model.ReviewPullRequest {
	t.Helper()
	pr := &model.ReviewPullRequest{
		IntegrationID:  model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName:   repo,
		PRNumber:       prNumber,
		ProjectID:      projectID,
		UserID:         userID,
		InstallationID: 1,
		RoundNumber:    round,
		HeadSHA:        "sha-head",
		CodeStatus:     "success",
		VisualStatus:   "failure",
		UpdatedAt:      time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(pr)
	}
	if err := db.Create(pr).Error; err != nil {
		t.Fatalf("create review pr: %v", err)
	}
	return pr
}

// createTestReviewRun 落一条运行记录。
func createTestReviewRun(t *testing.T, db *gorm.DB, userID, projectID string, opts ...func(*model.ReviewRun)) *model.ReviewRun {
	t.Helper()
	now := time.Now().UTC()
	r := &model.ReviewRun{
		IntegrationID:    model.ForgeIntegrationBuiltinGitHubID,
		ID:               uuid.NewString(),
		UserID:           userID,
		ProjectID:        projectID,
		Kind:             model.ReviewKindCode,
		RepoFullName:     "owner/repo",
		PRNumber:         1,
		HeadSHA:          "sha-head",
		RoundNumber:      1,
		PRTitle:          "PR title",
		PRURL:            "https://github.com/owner/repo/pull/1",
		EnvironmentID:    "env-1",
		InstanceID:       "claude",
		Model:            "claude-sonnet-4",
		ThreadID:         "thread-" + uuid.NewString(),
		LaunchCommandID:  "cmd-" + uuid.NewString(),
		InitialMessageID: "msg-" + uuid.NewString(),
		Status:           model.ReviewRunCompleted,
		DeadlineAt:       now.Add(time.Hour),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	for _, opt := range opts {
		opt(r)
	}
	if err := db.Create(r).Error; err != nil {
		t.Fatalf("create review run: %v", err)
	}
	return r
}

// 两个 PR、两个轮次：code/visual 分组正确、findings_count 从 result_json 解析
// （代码计 P0/P1/P2、视觉计 blocker/worth_fixing）。
func TestListReviewRecords_TwoPRsTwoRounds(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env-name")
	repo := "owner/repo"

	old := time.Now().UTC().Add(-time.Hour)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 2, func(pr *model.ReviewPullRequest) {
		pr.UpdatedAt = old
	})
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(pr *model.ReviewPullRequest) {
		pr.UpdatedAt = time.Now().UTC()
		pr.CodeStatus = "pending"
		pr.VisualStatus = ""
	})

	codeResult := `{"summary":"ok","findings":[
		{"level":"P0","file":"a.go","body":"x"},
		{"level":"P2","file":"b.go","body":"y"},
		{"level":"Note","file":"c.go","body":"n"}],
		"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`
	visualResult := `{"summary":"v","findings":[
		{"severity":"blocker","screenshot":"s1","dimension":"spacing","body":"b","fix":"f"},
		{"severity":"minor","screenshot":"s2","dimension":"color","body":"b","fix":"f"},
		{"severity":"worth_fixing","screenshot":"s3","dimension":"copy","body":"b","fix":"f"}],
		"prior":[],"unmeasurable":[]}`

	// PR1 round1：code + visual 各一；round2：code 一。
	createTestReviewRun(t, svc.db, user.ID, project.ID, func(r *model.ReviewRun) {
		r.EnvironmentID = env.ID
		r.ResultJSON = codeResult
		r.CreatedAt = old
	})
	createTestReviewRun(t, svc.db, user.ID, project.ID, func(r *model.ReviewRun) {
		r.Kind = model.ReviewKindVisual
		r.EnvironmentID = env.ID
		r.ResultJSON = visualResult
		r.CreatedAt = old.Add(time.Minute)
	})
	createTestReviewRun(t, svc.db, user.ID, project.ID, func(r *model.ReviewRun) {
		r.RoundNumber = 2
		r.ResultJSON = `{"summary":"r2","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	})
	// PR2 round1：code 一。
	createTestReviewRun(t, svc.db, user.ID, project.ID, func(r *model.ReviewRun) {
		r.PRNumber = 2
		r.PRTitle = "second"
		r.PRURL = "https://github.com/owner/repo/pull/2"
	})

	rs, _ := readyReviewService(t, svc)
	items, err := rs.ListReviewRecords(context.Background(), user.ID, project.ID, 0)
	if err != nil {
		t.Fatalf("ListReviewRecords: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	// updated_at DESC：PR2 在前。
	if items[0].PrNumber != 2 || items[1].PrNumber != 1 {
		t.Fatalf("排序错误: %d,%d", items[0].PrNumber, items[1].PrNumber)
	}
	if items[0].PrTitle != "second" || items[0].CodeStatus == nil || *items[0].CodeStatus != "pending" {
		t.Fatalf("PR2 记录错误: %+v", items[0])
	}
	if items[0].VisualStatus != nil {
		t.Fatalf("空 visual_status 应为 nil: %+v", items[0].VisualStatus)
	}

	pr1 := items[1]
	if len(pr1.Rounds) != 2 {
		t.Fatalf("rounds = %d, want 2", len(pr1.Rounds))
	}
	// round_number DESC：round2 在前。
	if pr1.Rounds[0].RoundNumber != 2 || pr1.Rounds[1].RoundNumber != 1 {
		t.Fatalf("轮次排序错误: %d,%d", pr1.Rounds[0].RoundNumber, pr1.Rounds[1].RoundNumber)
	}
	if len(pr1.Rounds[0].CodeRuns) != 1 || len(pr1.Rounds[0].VisualRuns) != 0 {
		t.Fatalf("round2 分组错误: %+v", pr1.Rounds[0])
	}
	r1 := pr1.Rounds[1]
	if len(r1.CodeRuns) != 1 || len(r1.VisualRuns) != 1 {
		t.Fatalf("round1 分组错误: %+v", r1)
	}
	if r1.CodeRuns[0].FindingsCount == nil || *r1.CodeRuns[0].FindingsCount != 2 {
		t.Fatalf("code findings_count = %v, want 2", r1.CodeRuns[0].FindingsCount)
	}
	if r1.VisualRuns[0].FindingsCount == nil || *r1.VisualRuns[0].FindingsCount != 2 {
		t.Fatalf("visual findings_count = %v, want 2", r1.VisualRuns[0].FindingsCount)
	}
	if r1.CodeRuns[0].EnvironmentName == nil || *r1.CodeRuns[0].EnvironmentName != "env-name" {
		t.Fatalf("environment_name 未补: %+v", r1.CodeRuns[0].EnvironmentName)
	}
	if r1.CodeRuns[0].UiChanged == nil || !*r1.CodeRuns[0].UiChanged {
		t.Fatalf("ui_changed 应为 true: %+v", r1.CodeRuns[0].UiChanged)
	}
	if r1.VisualRuns[0].UiChanged != nil {
		t.Fatalf("视觉运行不应有 ui_changed: %+v", r1.VisualRuns[0].UiChanged)
	}

	// 其他用户的项目 → 40401。
	if _, err := rs.ListReviewRecords(context.Background(), user.ID, "nonexistent", 0); appErrorCode(t, err) != 40401 {
		t.Fatalf("code = %d, want 40401", appErrorCode(t, err))
	}
}

// 总览：有仓库的项目列出开关/组合数/最近状态；同仓库被他人启用时
// taken_by 非空且 can_manage=false。
func TestListReviewProjects_TakenBy(t *testing.T) {
	svc := setupTestServices(t)
	me := createTestUser(t, svc.db, "me")
	other := createTestUser(t, svc.db, "other")

	myProject := createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = "octo"
		p.GithubRepo = "repo"
	})
	otherProject := createTestProject(t, svc.db, other.ID, func(p *model.Project) {
		p.GithubOwner = "OCTO"
		p.GithubRepo = "REPO"
	})
	// 无仓库项目不进总览。
	createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})

	// 我的项目未开启，对方项目已开启同仓库（占用判定按设置行的
	// repo_full_name 快照，与大写无关）。
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     otherProject.ID, UserID: other.ID, RepoFullName: "octo/repo", Enabled: true,
		TriggerOpened: true, TriggerSynchronize: true,
		CodeCombinationsJSON: `[{"environment_id":"e1","instance_id":"i","model":"m","t3_project_id":"t3p"}]`,
		CreatedAt:            now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// 我的项目没有开启（停用）但保存过设置：最近状态按这条设置行的快照挑账本。
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     myProject.ID, UserID: me.ID, RepoFullName: "octo/repo", Enabled: false,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	createTestReviewPR(t, svc.db, me.ID, myProject.ID, "octo/repo", 7, 1)

	rs, _ := readyReviewService(t, svc)
	items, err := rs.ListReviewProjects(context.Background(), me.ID)
	if err != nil {
		t.Fatalf("ListReviewProjects: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1（仅带仓库项目）", len(items))
	}
	got := items[0]
	if got.ProjectId != myProject.ID || got.Enabled {
		t.Fatalf("行内容错误: %+v", got)
	}
	if got.TakenBy == nil || *got.TakenBy != "user_other" {
		t.Fatalf("taken_by = %v, want user_other", got.TakenBy)
	}
	if got.CanManage {
		t.Fatalf("can_manage 应为 false")
	}
	if got.CodeStatus == nil || *got.CodeStatus != "success" || got.LastReviewAt == nil {
		t.Fatalf("最近状态未取到: %+v", got)
	}
	if got.VisualStatus == nil || *got.VisualStatus != "failure" {
		t.Fatalf("visual_status 未取到: %+v", got)
	}
}

// 总览的最近状态只认项目当前仓库：换过仓库后，旧仓库的历史记录不能冒充
// 新仓库的最近审查状态。
func TestListReviewProjects_StatusIgnoresHistoricalRepo(t *testing.T) {
	svc := setupTestServices(t)
	me := createTestUser(t, svc.db, "overview-history")
	project := createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = "octo"
		p.GithubRepo = "new"
	})
	now := time.Now().UTC()
	// 当前审查仓库由设置行的 repo_full_name 决定（停用也保留设置行）。
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     project.ID, UserID: me.ID, RepoFullName: "octo/new", Enabled: false,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// 旧仓库的记录更新时间更晚，也不能被选中。
	if err := svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     project.ID, RepoFullName: "octo/old", PRNumber: 1, UserID: me.ID,
		HeadSHA: "sha-old", CodeStatus: "failure", VisualStatus: "failure", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed old ledger: %v", err)
	}
	if err := svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     project.ID, RepoFullName: "octo/new", PRNumber: 2, UserID: me.ID,
		HeadSHA: "sha-new", CodeStatus: "success", UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed new ledger: %v", err)
	}

	rs, _ := readyReviewService(t, svc)
	items, err := rs.ListReviewProjects(context.Background(), me.ID)
	if err != nil {
		t.Fatalf("ListReviewProjects: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	got := items[0]
	if got.CodeStatus == nil || *got.CodeStatus != "success" {
		t.Fatalf("code_status = %v, want success（新仓库的账本）", got.CodeStatus)
	}
	if got.VisualStatus != nil {
		t.Fatalf("visual_status = %v, 旧仓库记录不应参与展示", *got.VisualStatus)
	}
}

// 总览的最近状态只按设置行快照挑账本：没有设置行的项目不可能有账本（开轮要求
// 设置行存在），即使库里有残留账本也不拿来当最近状态。
func TestListReviewProjects_StatusRequiresSettingsRow(t *testing.T) {
	svc := setupTestServices(t)
	me := createTestUser(t, svc.db, "overview-no-settings")
	project := createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = "octo"
		p.GithubRepo = "repo"
	})
	createTestReviewPR(t, svc.db, me.ID, project.ID, "octo/repo", 3, 1)

	rs, _ := readyReviewService(t, svc)
	items, err := rs.ListReviewProjects(context.Background(), me.ID)
	if err != nil {
		t.Fatalf("ListReviewProjects: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if got := items[0]; got.CodeStatus != nil || got.LastReviewAt != nil {
		t.Fatalf("无设置行时最近状态应留空, got %+v", got)
	}
}

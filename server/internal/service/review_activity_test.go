package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// testActivityCombosJSON n 个组合的落库 JSON（环境 id 依次编号）。
func testActivityCombosJSON(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"environment_id":"env-%d","instance_id":"claude","model":"claude-sonnet-4"}`, i+1)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// createActivitySettings 落一条审查设置行（repo 小写，与 webhook 同口径）。
func createActivitySettings(t *testing.T, db *gorm.DB, userID, projectID, repo string, codeCombos, visualCombos int, enabled bool) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.ProjectReviewSettings{
		ProjectID: projectID, UserID: userID, RepoFullName: repo, Enabled: enabled,
		TriggerOpened: true, TriggerSynchronize: true,
		CodeCombinationsJSON:   testActivityCombosJSON(codeCombos),
		VisualCombinationsJSON: testActivityCombosJSON(visualCombos),
		CreatedAt:              now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}
}

// createActivityDebounce 落一条防抖行。
func createActivityDebounce(t *testing.T, db *gorm.DB, projectID, repo string, prNumber int, opts ...func(*model.ReviewDebounce)) *model.ReviewDebounce {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	d := &model.ReviewDebounce{
		ProjectID: projectID, RepoFullName: repo, PRNumber: prNumber,
		HeadSHA: "sha-new", Action: "synchronize", InstallationID: 1,
		PRTitle: "debounce title",
		PRURL:   fmt.Sprintf("https://github.com/%s/pull/%d", repo, prNumber),
		FireAt:  now.Add(40 * time.Second), UpdatedAt: now,
	}
	for _, opt := range opts {
		opt(d)
	}
	if err := db.Create(d).Error; err != nil {
		t.Fatalf("create debounce: %v", err)
	}
	return d
}

// createActivityRun 落一条与指定账本同键的运行（对齐 repo/pr_number，
// head 默认与 createTestReviewPR 一致为 sha-head）。
func createActivityRun(t *testing.T, db *gorm.DB, userID, projectID, repo string, prNumber int, opts ...func(*model.ReviewRun)) *model.ReviewRun {
	t.Helper()
	return createTestReviewRun(t, db, userID, projectID, append([]func(*model.ReviewRun){
		func(r *model.ReviewRun) {
			r.RepoFullName = repo
			r.PRNumber = prNumber
		},
	}, opts...)...)
}

// listActivity 调 ListPullRequestActivity 并要求成功。
func listActivity(t *testing.T, rs *ReviewService, userID, projectID string, limit, offset int) *api.ReviewPullRequestActivityPage {
	t.Helper()
	page, err := rs.ListPullRequestActivity(context.Background(), userID, projectID, limit, offset)
	if err != nil {
		t.Fatalf("ListPullRequestActivity: %v", err)
	}
	return page
}

// 防抖优先：有防抖行时整行是「防抖中」，标题/URL/head 取防抖行，无阶段、
// 无发现、无 issue，started_at 是防抖行更新时刻。
func TestListPullRequestActivity_Debouncing(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 1, true)
	pr := createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	debounce := createActivityDebounce(t, svc.db, project.ID, repo, 1)

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 1 || page.Counts.InProgress != 1 || page.Counts.NeedsAction != 0 || page.Counts.RecentlyPassed != 0 {
		t.Fatalf("counts 错误: total=%d %+v", page.Total, page.Counts)
	}
	item := page.Items[0]
	if item.Group != api.ReviewPullRequestActivityGroupInProgress ||
		item.Stage == nil || *item.Stage != api.ReviewPullRequestActivityStageDebouncing {
		t.Fatalf("分组/阶段错误: %+v", item)
	}
	if item.DebounceFireAt == nil || *item.DebounceFireAt != debounce.FireAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("debounce_fire_at 错误: %v", item.DebounceFireAt)
	}
	if item.PrTitle != "debounce title" || item.PrUrl != debounce.PRURL || item.HeadSha != "sha-new" {
		t.Fatalf("快照应取防抖行: %+v", item)
	}
	if len(item.Stages) != 0 || len(item.Issues) != 0 {
		t.Fatalf("防抖中不应有 stages/issues: %+v %+v", item.Stages, item.Issues)
	}
	if item.Findings != (api.ReviewFindingCounts{}) {
		t.Fatalf("findings 应全 0: %+v", item.Findings)
	}
	if item.StartedAt != debounce.UpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("started_at 应为防抖行 updated_at: %s", item.StartedAt)
	}
	if item.RoundNumber != pr.RoundNumber || !item.CanRerun {
		t.Fatalf("round/can_rerun 错误: %+v", item)
	}
}

// 排队与审查中：全部非终态 run 都是 queued → stage=queued；出现
// dispatched/running → stage=reviewing。执行状态只看 runs。
func TestListPullRequestActivity_QueuedAndReviewing(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 0, true)
	// PR1：两个 queued run；PR2：一个 dispatched run。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	for i := 0; i < 2; i++ {
		createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
			r.Status = model.ReviewRunQueued
		})
	}
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 2, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunDispatched
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.InProgress != 2 {
		t.Fatalf("in_progress = %d, want 2", page.Counts.InProgress)
	}
	byPR := map[int]api.ReviewPullRequestActivity{}
	for _, it := range page.Items {
		byPR[it.PrNumber] = it
	}
	queued := byPR[1]
	if queued.Stage == nil || *queued.Stage != api.ReviewPullRequestActivityStageQueued {
		t.Fatalf("PR1 应为 queued: %+v", queued.Stage)
	}
	if len(queued.Stages) != 1 || queued.Stages[0].Kind != api.ReviewStageProgressKindCode ||
		queued.Stages[0].Total != 2 || queued.Stages[0].Completed != 0 ||
		queued.Stages[0].Status != api.ReviewStageProgressStatusQueued {
		t.Fatalf("PR1 code 段错误: %+v", queued.Stages)
	}
	reviewing := byPR[2]
	if reviewing.Stage == nil || *reviewing.Stage != api.ReviewPullRequestActivityStageReviewing {
		t.Fatalf("PR2 应为 reviewing: %+v", reviewing.Stage)
	}
	if reviewing.Stages[0].Status != api.ReviewStageProgressStatusRunning {
		t.Fatalf("PR2 code 段应为 running: %+v", reviewing.Stages[0])
	}
}

// 发现计数与待处理：completed run 解析 result_json 计 P0/P1/P2 与
// blocker/worth_fixing（minor 不计）；有发现即 needs_action。
func TestListPullRequestActivity_NeedsActionFindings(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 1, true)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "failure"
		p.VisualStatus = "success"
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = `{"summary":"s","findings":[
			{"level":"P0","file":"a.go","body":"x"},
			{"level":"P1","file":"b.go","body":"y"},
			{"level":"P2","file":"c.go","body":"z"},
			{"level":"Note","file":"d.go","body":"n"}],
			"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`
		r.FinishedAt = api.Ptr(time.Now().UTC())
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Kind = model.ReviewKindVisual
		r.ResultJSON = `{"summary":"v","findings":[
			{"severity":"blocker","screenshot":"s1","dimension":"spacing","body":"b","fix":"f"},
			{"severity":"minor","screenshot":"s2","dimension":"color","body":"b","fix":"f"},
			{"severity":"worth_fixing","screenshot":"s3","dimension":"copy","body":"b","fix":"f"}],
			"prior":[],"unmeasurable":[]}`
		r.FinishedAt = api.Ptr(time.Now().UTC())
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.NeedsAction != 1 {
		t.Fatalf("needs_action = %d, want 1", page.Counts.NeedsAction)
	}
	item := page.Items[0]
	want := api.ReviewFindingCounts{P0: 1, P1: 1, P2: 1, Blocker: 1, WorthFixing: 1}
	if item.Findings != want {
		t.Fatalf("findings = %+v, want %+v", item.Findings, want)
	}
	if len(item.Issues) != 0 {
		t.Fatalf("有发现时不应有 run issue: %+v", item.Issues)
	}
	// code_status=failure 但发现计数>0 → 不加账本兜底 issue。
}

// 失败运行：dispatch_failed→派发失败、timeout→超时；cancelled 一律忽略
// （不产生 issue，也不把已终态 run 判成进行中）。
func TestListPullRequestActivity_RunFailuresAndCancelled(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 0, true)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunFailed
		r.ErrorCode = "dispatch_failed"
		r.ErrorMessage = "环境离线，无法创建线程"
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunTimedOut
		r.ErrorCode = "timeout"
		r.ErrorMessage = "审查超时"
	})
	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.NeedsAction != 1 || page.Counts.InProgress != 0 {
		t.Fatalf("counts 错误: %+v", page.Counts)
	}
	item := page.Items[0]
	if len(item.Issues) != 2 {
		t.Fatalf("issues = %d, want 2: %+v", len(item.Issues), item.Issues)
	}
	reasons := map[string]api.ReviewActivityIssue{}
	for _, it := range item.Issues {
		reasons[it.Reason] = it
		if it.Kind != api.ReviewActivityIssueKindCode {
			t.Fatalf("issue kind 错误: %+v", it)
		}
		if it.Combination == nil || *it.Combination != "claude-sonnet-4 · claude" {
			t.Fatalf("combination 错误: %+v", it.Combination)
		}
	}
	if got := reasons[reviewIssueReasonDispatchFailed]; got.Message != "环境离线，无法创建线程" {
		t.Fatalf("派发失败 message 错误: %+v", got)
	}
	if got := reasons[reviewIssueReasonTimeout]; got.Message != "审查超时" {
		t.Fatalf("超时 message 错误: %+v", got)
	}
}

// 结论无法解析：completed 但 result_json 非法 → 「结论无法解析」issue。
func TestListPullRequestActivity_UnparsedResult(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = "not-json"
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.NeedsAction != 1 {
		t.Fatalf("needs_action = %d, want 1", page.Counts.NeedsAction)
	}
	issues := page.Items[0].Issues
	if len(issues) != 1 || issues[0].Reason != reviewIssueReasonUnparsed ||
		issues[0].Message != "审查结论无法解析，请查看 GitHub" {
		t.Fatalf("issues 错误: %+v", issues)
	}
}

// 账本兜底（保守文案）：code/visual 的 failure 与 error 在没有运行级 issue
// 时各补一条；failure 但发现计数>0 不补。
func TestListPullRequestActivity_LedgerFallbacks(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 1, true)
	// PR1：code_status=error；PR2：visual_status=error；PR3：visual_status=failure。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "error"
		p.VisualStatus = ""
	})
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = "error"
	})
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 3, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = "failure"
	})
	for _, n := range []int{1, 2, 3} {
		createActivityRun(t, svc.db, user.ID, project.ID, repo, n, func(r *model.ReviewRun) {
			r.ResultJSON = `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
		})
	}

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.NeedsAction != 3 || page.Counts.RecentlyPassed != 0 {
		t.Fatalf("counts 错误: %+v", page.Counts)
	}
	byPR := map[int][]api.ReviewActivityIssue{}
	for _, it := range page.Items {
		byPR[it.PrNumber] = it.Issues
	}
	if len(byPR[1]) != 1 || byPR[1][0].Reason != reviewIssueReasonNotFinished ||
		byPR[1][0].Message != "代码审查未完成" || byPR[1][0].Kind != api.ReviewActivityIssueKindCode {
		t.Fatalf("PR1 兜底错误: %+v", byPR[1])
	}
	if byPR[1][0].Combination != nil {
		t.Fatalf("账本兜底 issue 不应带组合: %+v", byPR[1][0])
	}
	if len(byPR[2]) != 1 || byPR[2][0].Reason != reviewIssueReasonNotFinished ||
		byPR[2][0].Message != "视觉审查未完成" || byPR[2][0].Kind != api.ReviewActivityIssueKindVisual {
		t.Fatalf("PR2 兜底错误: %+v", byPR[2])
	}
	if len(byPR[3]) != 1 || byPR[3][0].Reason != reviewIssueReasonVisualFailure ||
		!strings.Contains(byPR[3][0].Message, "截图不可用") {
		t.Fatalf("PR3 兜底错误: %+v", byPR[3])
	}
}

// head 不匹配的旧轮不混入；动作未勾选（head 已推进但没开轮）不返回。
func TestListPullRequestActivity_CurrentRoundOnly(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	// PR1：账本已推进到 sha-new/round2，但只有旧 head 的 round1 run。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 2, func(p *model.ReviewPullRequest) {
		p.HeadSHA = "sha-new"
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.HeadSHA = "sha-old"
		r.RoundNumber = 1
	})
	// PR2：没有任何 run（新 head 未开轮）。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("不应返回任何 PR: total=%d items=%d", page.Total, len(page.Items))
	}
}

// 最近通过 7 天口径：完成的进入 recently_passed；结论早于 7 天的不返回；
// finished_at 缺失时回退账本 updated_at。
func TestListPullRequestActivity_RecentWindow(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	clean := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	// PR1：昨天完成 → recently_passed。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = clean
		r.FinishedAt = api.Ptr(time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second))
	})
	// PR2：8 天前完成 → 不返回。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 2, func(r *model.ReviewRun) {
		r.ResultJSON = clean
		r.FinishedAt = api.Ptr(time.Now().UTC().Add(-8 * 24 * time.Hour))
	})
	// PR3：没有 finished_at，回退账本 updated_at（昨天）→ recently_passed。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 3, 1, func(p *model.ReviewPullRequest) {
		p.UpdatedAt = time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 3, func(r *model.ReviewRun) {
		r.ResultJSON = clean
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 2 || page.Counts.RecentlyPassed != 2 {
		t.Fatalf("counts 错误: total=%d %+v", page.Total, page.Counts)
	}
	// 最近通过按结论完成时刻 DESC：PR1（有 finished_at）在 PR3 之前。
	if page.Items[0].PrNumber != 1 || page.Items[1].PrNumber != 3 {
		t.Fatalf("排序错误: %d,%d", page.Items[0].PrNumber, page.Items[1].PrNumber)
	}
}

// 项目筛选：只返回指定项目范围内的 PR；别人的项目 40401。
func TestListPullRequestActivity_ProjectFilter(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	other := createTestUser(t, svc.db, "u2")
	projectA := createTestProject(t, svc.db, user.ID)
	projectB := createTestProject(t, svc.db, user.ID)
	foreign := createTestProject(t, svc.db, other.ID)
	repo := "octo/repo"
	for _, p := range []model.Project{*projectA, *projectB} {
		createActivitySettings(t, svc.db, user.ID, p.ID, repo, 1, 0, true)
		createTestReviewPR(t, svc.db, user.ID, p.ID, repo, 1, 1, func(pr *model.ReviewPullRequest) {
			pr.VisualStatus = ""
		})
		createActivityRun(t, svc.db, user.ID, p.ID, repo, 1, func(r *model.ReviewRun) {
			r.Status = model.ReviewRunQueued
		})
	}
	createActivitySettings(t, svc.db, other.ID, foreign.ID, repo, 1, 0, true)
	createTestReviewPR(t, svc.db, other.ID, foreign.ID, repo, 1, 1)
	createActivityRun(t, svc.db, other.ID, foreign.ID, repo, 1)

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, projectA.ID, 0, 0)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ProjectId != projectA.ID {
		t.Fatalf("筛选后应只剩项目 A 的 PR: %+v", page)
	}
	if page.Items[0].ProjectName == "" {
		t.Fatalf("project_name 未填: %+v", page.Items[0])
	}
	if _, err := rs.ListPullRequestActivity(context.Background(), user.ID, foreign.ID, 0, 0); appErrorCode(t, err) != 40401 {
		t.Fatalf("code = %d, want 40401", appErrorCode(t, err))
	}
}

// counts/total 是过滤范围内全量统计，items 按 offset/limit 切片；
// 组间顺序固定为进行中 → 需要处理 → 最近通过。
func TestListPullRequestActivity_CountsTotalPagination(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	clean := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	// 进行中：queued。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunQueued
	})
	// 需要处理：P1。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "failure"
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 2, func(r *model.ReviewRun) {
		r.ResultJSON = `{"summary":"s","findings":[{"level":"P1","file":"a.go","body":"x"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	})
	// 最近通过。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 3, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 3, func(r *model.ReviewRun) {
		r.ResultJSON = clean
		r.FinishedAt = api.Ptr(time.Now().UTC())
	})

	rs, _ := readyReviewService(t, svc)
	full := listActivity(t, rs, user.ID, "", 0, 0)
	if full.Total != 3 || full.Counts.InProgress != 1 || full.Counts.NeedsAction != 1 || full.Counts.RecentlyPassed != 1 {
		t.Fatalf("counts 错误: total=%d %+v", full.Total, full.Counts)
	}
	wantOrder := []int{1, 2, 3}
	for i, n := range wantOrder {
		if full.Items[i].PrNumber != n {
			t.Fatalf("顺序错误: %+v", full.Items)
		}
	}
	// 分页：limit=1 逐页切。
	for offset, want := range map[int]int{0: 1, 1: 2, 2: 3} {
		page := listActivity(t, rs, user.ID, "", 1, offset)
		if len(page.Items) != 1 || page.Items[0].PrNumber != want || page.Total != 3 {
			t.Fatalf("offset=%d 页错误: %+v", offset, page)
		}
	}
	// 越界 offset → 空 items，counts/total 不变。
	empty := listActivity(t, rs, user.ID, "", 50, 3)
	if len(empty.Items) != 0 || empty.Total != 3 || empty.Counts.InProgress != 1 {
		t.Fatalf("越界页错误: %+v", empty)
	}
}

// 视觉段进度：有运行照代码聚合；配置>0 无运行时按代码段状态推
// pending/skipped/failed；配置为 0 不返回视觉段。
func TestListPullRequestActivity_VisualStage(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 1, true)
	clean := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	p1 := `{"summary":"s","findings":[{"level":"P1","file":"a.go","body":"x"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`

	// PR1：代码 queued、视觉未建 → 视觉 pending（等待代码审查）。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunQueued
	})
	// PR2：代码完成且无发现、visual_status=success（无 UI 改动）→ skipped，
	// 整行进 recently_passed。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = "success"
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 2, func(r *model.ReviewRun) {
		r.ResultJSON = clean
	})
	// PR3：代码完成但有问题、visual_status=success → 视觉 skipped。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 3, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "failure"
		p.VisualStatus = "success"
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 3, func(r *model.ReviewRun) {
		r.ResultJSON = p1
	})
	// PR4：visual_status=error、代码干净 → 视觉 failed。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 4, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = "error"
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 4, func(r *model.ReviewRun) {
		r.ResultJSON = clean
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	byPR := map[int]api.ReviewPullRequestActivity{}
	for _, it := range page.Items {
		byPR[it.PrNumber] = it
	}
	stages := byPR[1].Stages
	if len(stages) != 2 || stages[1].Kind != api.ReviewStageProgressKindVisual ||
		stages[1].Total != 1 || stages[1].Status != api.ReviewStageProgressStatusPending {
		t.Fatalf("PR1 视觉段应为 pending: %+v", stages)
	}
	if got := byPR[2].Stages[1].Status; got != api.ReviewStageProgressStatusSkipped {
		t.Fatalf("PR2 视觉段应为 skipped: %v", got)
	}
	if got := byPR[3].Stages[1].Status; got != api.ReviewStageProgressStatusSkipped {
		t.Fatalf("PR3 视觉段应为 skipped: %v", got)
	}
	if got := byPR[4].Stages[1].Status; got != api.ReviewStageProgressStatusFailed {
		t.Fatalf("PR4 视觉段应为 failed: %v", got)
	}

	// 配置为 0 的项目：只有 code 段。
	projectB := createTestProject(t, svc.db, user.ID)
	createActivitySettings(t, svc.db, user.ID, projectB.ID, repo, 1, 0, true)
	createTestReviewPR(t, svc.db, user.ID, projectB.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, projectB.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunQueued
	})
	pageB := listActivity(t, rs, user.ID, projectB.ID, 0, 0)
	if len(pageB.Items[0].Stages) != 1 {
		t.Fatalf("无视觉配置不应有视觉段: %+v", pageB.Items[0].Stages)
	}
}

// 停用项目不出现在首页（范围是 enabled=true 的设置行）。
func TestListPullRequestActivity_DisabledProjectExcluded(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, false)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1)
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1)

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 0 {
		t.Fatalf("停用项目不应出现在首页: %+v", page)
	}
}

// 详情：项目归属 40401；设置行/账本行缺失 40434；不要求 enabled；输出与
// review-records 同结构，带 trigger_action、findings、unmeasurable。
func TestGetPullRequestRecord(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	other := createTestUser(t, svc.db, "u2")
	project := createTestProject(t, svc.db, user.ID)
	foreign := createTestProject(t, svc.db, other.ID)
	repo := "octo/repo"
	env := createT3TestEnv(t, svc.db, user.ID, "env-name")
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 1, true)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})

	codeResult := `{"summary":"ok","findings":[
		{"level":"P1","file":"a.go","line":12,"body":"x"},
		{"level":"Note","file":"b.go","body":"n"}],
		"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`
	visualResult := `{"summary":"v","findings":[
		{"severity":"blocker","screenshot":"s1","dimension":"spacing","body":"b","fix":"align"}],
		"prior":[],"unmeasurable":[{"screenshot":"s2","reason":"加载失败"}]}`
	now := time.Now().UTC().Truncate(time.Second)
	// round1：两条代码 run（触发方式 opened / synchronize）+ 一条视觉 run。
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.TriggerAction = "opened"
		r.EnvironmentID = env.ID
		r.ResultJSON = codeResult
		r.CreatedAt = now
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.TriggerAction = "synchronize"
		r.CreatedAt = now.Add(time.Minute)
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Kind = model.ReviewKindVisual
		r.TriggerAction = "visual"
		r.ResultJSON = visualResult
		r.CreatedAt = now.Add(2 * time.Minute)
	})

	rs, _ := readyReviewService(t, svc)
	rec, err := rs.GetPullRequestRecord(context.Background(), user.ID, project.ID, 1)
	if err != nil {
		t.Fatalf("GetPullRequestRecord: %v", err)
	}
	if rec.PrNumber != 1 || rec.RepoFullName != repo || !rec.CanRerun {
		t.Fatalf("记录头错误: %+v", rec)
	}
	if len(rec.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(rec.Rounds))
	}
	round := rec.Rounds[0]
	// trigger_action 取该轮第一条代码 run。
	if round.TriggerAction != api.ReviewRoundRecordTriggerActionOpened {
		t.Fatalf("trigger_action = %v, want opened", round.TriggerAction)
	}
	if len(round.CodeRuns) != 2 || len(round.VisualRuns) != 1 {
		t.Fatalf("分组错误: %+v", round)
	}
	first := round.CodeRuns[0]
	// findings_count 只计 P0/P1/P2（Note 不计）。
	if first.FindingsCount == nil || *first.FindingsCount != 1 {
		t.Fatalf("findings_count = %v, want 1", first.FindingsCount)
	}
	if len(first.Findings) != 2 || first.Findings[0].Level != "P1" ||
		first.Findings[0].File == nil || *first.Findings[0].File != "a.go" ||
		first.Findings[0].Line == nil || *first.Findings[0].Line != 12 ||
		first.Findings[1].Level != "Note" {
		t.Fatalf("代码发现投影错误: %+v", first.Findings)
	}
	if first.EnvironmentName == nil || *first.EnvironmentName != "env-name" {
		t.Fatalf("environment_name 未补: %+v", first.EnvironmentName)
	}
	visual := round.VisualRuns[0]
	if len(visual.Findings) != 1 || visual.Findings[0].Level != "blocker" ||
		visual.Findings[0].Dimension == nil || *visual.Findings[0].Dimension != "spacing" ||
		visual.Findings[0].Fix == nil || *visual.Findings[0].Fix != "align" ||
		visual.Findings[0].Screenshot == nil || *visual.Findings[0].Screenshot != "s1" {
		t.Fatalf("视觉发现投影错误: %+v", visual.Findings)
	}
	if len(visual.Unmeasurable) != 1 || visual.Unmeasurable[0].Screenshot != "s2" {
		t.Fatalf("unmeasurable 投影错误: %+v", visual.Unmeasurable)
	}
	if visual.UiChanged != nil {
		t.Fatalf("视觉运行不应有 ui_changed: %+v", visual.UiChanged)
	}

	// 没有代码 run 的轮次 trigger_action 回退 unknown。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 2, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 2, func(r *model.ReviewRun) {
		r.Kind = model.ReviewKindVisual
	})
	rec2, err := rs.GetPullRequestRecord(context.Background(), user.ID, project.ID, 2)
	if err != nil {
		t.Fatalf("GetPullRequestRecord PR2: %v", err)
	}
	if rec2.Rounds[0].TriggerAction != api.ReviewRoundRecordTriggerActionUnknown {
		t.Fatalf("无代码 run 应回退 unknown: %v", rec2.Rounds[0].TriggerAction)
	}

	// 停用项目仍可看详情（不要求 enabled）；can_rerun 沿用 review-records
	// 语义（只看仓库归属），停用拦截在重跑接口里做。
	if err := svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable settings: %v", err)
	}
	recDisabled, err := rs.GetPullRequestRecord(context.Background(), user.ID, project.ID, 1)
	if err != nil {
		t.Fatalf("停用项目详情: %v", err)
	}
	if len(recDisabled.Rounds) != 1 {
		t.Fatalf("停用项目详情轮次错误: %+v", recDisabled)
	}

	// 40434：无设置行 / 设置行仓库无此账本。
	bare := createTestProject(t, svc.db, user.ID)
	if _, err := rs.GetPullRequestRecord(context.Background(), user.ID, bare.ID, 1); appErrorCode(t, err) != 40434 {
		t.Fatalf("code = %d, want 40434（无设置行）", appErrorCode(t, err))
	}
	if _, err := rs.GetPullRequestRecord(context.Background(), user.ID, project.ID, 99); appErrorCode(t, err) != 40434 {
		t.Fatalf("code = %d, want 40434（无账本行）", appErrorCode(t, err))
	}
	// 40401：别人的项目。
	if _, err := rs.GetPullRequestRecord(context.Background(), user.ID, foreign.ID, 1); appErrorCode(t, err) != 40401 {
		t.Fatalf("code = %d, want 40401", appErrorCode(t, err))
	}
}

// 当前轮运行按项目键取：同仓库换项目接管后，两个项目可能留下同
// repo/pr/round/head 的运行；首页只能统计当前启用项目自己的运行，不能合并
// 另一个（已停用）项目的发现。
func TestListPullRequestActivity_RunKeyScopedToProject(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	other := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	createActivitySettings(t, svc.db, user.ID, other.ID, repo, 1, 0, false)
	clean := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	p0 := `{"summary":"s","findings":[{"level":"P0","file":"a.go","body":"x"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	// 当前项目：一条通过运行。
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = clean
	})
	// 接管前的旧项目：同号 PR、同轮、同 head 的 P0 运行。
	createTestReviewPR(t, svc.db, user.ID, other.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, other.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = p0
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("应只返回当前项目一行: total=%d items=%d", page.Total, len(page.Items))
	}
	item := page.Items[0]
	if item.ProjectId != project.ID {
		t.Fatalf("返回了其他项目的行: %s", item.ProjectId)
	}
	if item.Findings.P0 != 0 || item.Group != api.ReviewPullRequestActivityGroupRecentlyPassed {
		t.Fatalf("旧项目运行混入当前项目: findings=%+v group=%s", item.Findings, item.Group)
	}
	if len(item.Stages) == 0 || item.Stages[0].Total != 1 {
		t.Fatalf("代码段应只统计当前项目的 1 条运行: %+v", item.Stages)
	}
}

// 全部 cancelled 的轮次不算通过：账本可能残留 success，但没有 completed 的
// 代码运行时不进「最近通过」。
func TestListPullRequestActivity_AllCancelledNotPassed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 0, true)
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "success"
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunCancelled
		r.ErrorCode = "superseded"
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("全 cancelled 不应进入首页: total=%d items=%d", page.Total, len(page.Items))
	}
}

// 视觉衔接窗口：代码已完成且通过、代码结论 ui_changed=true、配置了视觉组合，
// 但视觉运行还没创建、账本 visual_status 仍为空 → 仍算进行中（审查中），
// 视觉段 pending，不能提前判「最近通过」让前端停掉轮询。
func TestListPullRequestActivity_VisualHandoffInProgress(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 1, 1, true)
	changed := `{"summary":"s","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "success"
		p.VisualStatus = ""
		p.SyncPending = true
		p.UpdatedAt = time.Now().UTC()
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = changed
		r.FinishedAt = api.Ptr(time.Now().UTC())
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 1 || page.Counts.InProgress != 1 || page.Counts.RecentlyPassed != 0 {
		t.Fatalf("视觉衔接窗口应算进行中: total=%d %+v", page.Total, page.Counts)
	}
	item := page.Items[0]
	if item.Group != api.ReviewPullRequestActivityGroupInProgress ||
		item.Stage == nil || *item.Stage != api.ReviewPullRequestActivityStageReviewing {
		t.Fatalf("分组/阶段错误: %+v", item)
	}
	if len(item.Stages) != 2 || item.Stages[0].Status != api.ReviewStageProgressStatusCompleted ||
		item.Stages[1].Status != api.ReviewStageProgressStatusPending {
		t.Fatalf("进度应为代码完成 + 视觉等待: %+v", item.Stages)
	}

	// 同步清掉 pending 后仍无视觉运行：视觉确实不启动 → 最近通过、视觉段未启动。
	if err := svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", project.ID, repo, 1).
		Update("sync_pending", false).Error; err != nil {
		t.Fatalf("clear sync_pending: %v", err)
	}
	page = listActivity(t, rs, user.ID, "", 0, 0)
	if page.Counts.RecentlyPassed != 1 || page.Counts.InProgress != 0 {
		t.Fatalf("同步完成后应为最近通过: %+v", page.Counts)
	}
	if len(page.Items[0].Stages) != 2 || page.Items[0].Stages[1].Status != api.ReviewStageProgressStatusSkipped {
		t.Fatalf("视觉段应未启动: %+v", page.Items[0].Stages)
	}
}

// 部分完成、其余取消的轮次不能算通过：一个组合干净完成、另一个在 PR 关闭时
// 被取消，本轮没有完整结论，不进首页（等 reopened/新推送/手动重跑）。
func TestListPullRequestActivity_PartialCancelledNotPassed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	repo := "octo/repo"
	createActivitySettings(t, svc.db, user.ID, project.ID, repo, 2, 0, true)
	clean := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	createTestReviewPR(t, svc.db, user.ID, project.ID, repo, 1, 1, func(p *model.ReviewPullRequest) {
		p.CodeStatus = "success"
		p.VisualStatus = ""
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.ResultJSON = clean
	})
	createActivityRun(t, svc.db, user.ID, project.ID, repo, 1, func(r *model.ReviewRun) {
		r.Status = model.ReviewRunCancelled
		r.ErrorCode = "superseded"
	})

	rs, _ := readyReviewService(t, svc)
	page := listActivity(t, rs, user.ID, "", 0, 0)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("部分取消不应进入首页: total=%d items=%d", page.Total, len(page.Items))
	}
}

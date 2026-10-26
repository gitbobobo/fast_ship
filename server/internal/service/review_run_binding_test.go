package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// INT-169 运行身份强校验与文件归属的 service 侧用例；中间件/路由绑定见
// handler 包 TestReviewAgentRunBinding。

// TestAgentSubmitResultFindingFileAttribution 代码审查新发现的文件必须在
// head_sha 树中：外仓/不存在路径 → 40004 且运行与令牌零副作用；仓内
// diff 外文件不因不在 diff 被拒；prior 里的已删除路径不查。
func TestAgentSubmitResultFindingFileAttribution(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 20, &githubapi.PullRequest{
		RepoFullName: repo, Number: 20, HeadSHA: "sha-t",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 20, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-t", RoundNumber: 1, UpdatedAt: now,
	})
	// 树里只有 a.go 与 docs/guide.md；fake 默认 diff 是 main.go——a.go
	// 不在 diff 仍须受理（不限定 diff 内文件）。索引按 (repo, ref) 建：
	// 另一 ref 下的路径不能借来通过。
	e.fake.SetTreeFiles(repo, "sha-t", "a.go", "docs/guide.md")
	e.fake.SetTreeFiles(repo, "sha-old", "moved.go")

	run := e.seedRun(t, repo, 20, "sha-t", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-file", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	// 不存在路径 → 40004 含「核对审查对象」；运行仍在途、令牌未消费。
	// 同组覆盖：另一 ref 下才存在的路径（moved.go@sha-old）与各类别名——
	// 前导 /、空段、. 段、.. 段都不做静默归一化，一律按不存在拒。
	for _, badFile := range []string{
		"other-repo/x.go", "moved.go", "../secret.key", "a.go/../guide.md",
		"/a.go", "a.go//sub.go", "./a.go", "a.go/./b.go", "",
	} {
		bad := `{"findings":[{"level":"P1","file":"` + badFile + `","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
		err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(bad))
		assertErrCode(t, err, 40004)
		// 空 file 在结构校验阶段被拒（40004「file 不能为空」），其余进树
		// 归属校验——必须带「核对审查对象」提示。
		if badFile != "" && !strings.Contains(err.Error(), "核对审查对象") {
			t.Fatalf("file %q: error = %v, want 核对审查对象 hint", badFile, err)
		}
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, file rejection must not change run", after.Status)
	}
	var tok model.ReviewRunToken
	e.svc.db.First(&tok, "run_id = ?", run.ID)
	if tok.ConsumedAt != nil {
		t.Fatal("token must stay unconsumed on file rejection")
	}

	// 存在路径（diff 外仓内文件）+ prior 里的已删除路径 → completed。
	good := `{"findings":[{"level":"P1","file":"a.go","body":"x"},{"level":"Note","file":"docs/guide.md","body":"y"}],"prior":[{"file":"deleted.go","body":"历史已删除路径","status":"fixed"}],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)); err != nil {
		t.Fatalf("submit good: %v", err)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCompleted {
		t.Fatalf("status = %q, want completed", after.Status)
	}

	// 同 canonical 重放不受新检查影响：清空该 ref 索引后重放仍幂等成功。
	e.fake.SetTreeFiles(repo, "sha-t")
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)); err != nil {
		t.Fatalf("idempotent replay must bypass file check: %v", err)
	}
}

// TestAgentSubmitResultDeletedFileFindings head 树未命中的路径用本轮 PR
// 变更文件清单兜底：PR 删除/重命名走的文件仍是合法审查对象（r2 P2）；
// 既不在树也不在清单里的路径照旧 40004。
func TestAgentSubmitResultDeletedFileFindings(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 27, &githubapi.PullRequest{
		RepoFullName: repo, Number: 27, HeadSHA: "sha-d",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 27, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-d", RoundNumber: 1, UpdatedAt: now,
	})
	// 树里只有 a.go；变更清单记录 gone.go 被删除、old.go→new.go 重命名
	// （files 端点两侧都收：filename=new.go、previous_filename=old.go）。
	e.fake.SetTreeFiles(repo, "sha-d", "a.go", "new.go")
	e.fake.SetPRFiles("gone.go", "old.go", "new.go")

	run := e.seedRun(t, repo, 27, "sha-d", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-del", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	// 已删文件 + 重命名旧路径的 finding 受理；幽灵路径仍 40004。
	bad := `{"findings":[{"level":"P1","file":"ghost.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(bad))
	if err == nil || !strings.Contains(err.Error(), "核对审查对象") {
		t.Fatalf("ghost path = %v, want 40004 核对审查对象", err)
	}
	good := `{"findings":[{"level":"P1","file":"gone.go","body":"不该删除此文件"},{"level":"P2","file":"old.go","body":"重命名后调用方未跟进"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)); err != nil {
		t.Fatalf("deleted/renamed file findings must be accepted: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCompleted {
		t.Fatalf("status = %q, want completed", after.Status)
	}
}

// TestAgentSubmitResultFilesListInconclusive PR 变更文件清单不完整（平台
// 封顶截断）时树未命中路径的「不在清单内」不是有效缺席证据——放行完成
// 而非 40004/50200 卡死；清单拉取失败仍按 50200 平台失败上报。
func TestAgentSubmitResultFilesListInconclusive(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 29, &githubapi.PullRequest{
		RepoFullName: repo, Number: 29, HeadSHA: "sha-l",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 29, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-l", RoundNumber: 1, UpdatedAt: now,
	})
	e.fake.SetTreeFiles(repo, "sha-l", "a.go")

	run := e.seedRun(t, repo, 29, "sha-l", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-large", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	body := `{"findings":[{"level":"P1","file":"gone.go","body":"不该删除此文件"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`

	// 清单拉取失败 → 50200，运行与令牌零副作用。
	e.fake.SetPRFilesResult(nil, false, errors.New("boom"))
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body))
	assertErrCode(t, err, 50200)
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, files list failure must not change run", mid.Status)
	}

	// 清单不完整（封顶截断）→ 兜底不可证，树未命中路径放行，运行正常完成。
	e.fake.SetPRFilesResult([]string{"a.go"}, false, nil)
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body)); err != nil {
		t.Fatalf("incomplete files list must not reject unverifiable path: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCompleted {
		t.Fatalf("status = %q, want completed", after.Status)
	}
}

// TestAgentSubmitResultTreeTruncatedInconclusive 树条目截断时「不在截断
// 清单内」不能当缺席证明（r4 P2）——按不可判定放行完成而非 50200 卡死
// （重试只拿到同一份截断树）；其它树错误仍 50200。
func TestAgentSubmitResultTreeTruncatedInconclusive(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 31, &githubapi.PullRequest{
		RepoFullName: repo, Number: 31, HeadSHA: "sha-tr",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 31, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-tr", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 31, "sha-tr", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-tr", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	body := `{"findings":[{"level":"P1","file":"big/f.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`

	// 非截断类树错误 → 50200，运行与令牌零副作用。
	e.fake.SetTreeError(repo, errors.New("github 5xx"))
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body))
	assertErrCode(t, err, 50200)
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, tree error must not change run", mid.Status)
	}

	// 截断（ErrTreeTruncated→适配器归一为 forge 哨兵）→ 不可判定放行完成。
	e.fake.SetTreeError(repo, githubapi.ErrTreeTruncated)
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body)); err != nil {
		t.Fatalf("truncated tree must not reject unverifiable path: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCompleted {
		t.Fatalf("status = %q, want completed", after.Status)
	}
}

// TestAgentSubmitResultFilesListFallback 树未命中路径的清单兜底边界
// （r10 P2 改源）：含空格/控制字符/分隔符形态的路径不再经 diff 文本
// 解析——files 端点清单原样收（含 rename 的 previous_filename 旧名），
// 清单外的伪造路径照旧 40004。
func TestAgentSubmitResultFilesListFallback(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 32, &githubapi.PullRequest{
		RepoFullName: repo, Number: 32, HeadSHA: "sha-e",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 32, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-e", RoundNumber: 1, UpdatedAt: now,
	})
	e.fake.SetTreeFiles(repo, "sha-e", "keep.go")
	// 已删/重命名走的特殊字符路径：清单原样收录即可受理，无解析歧义面。
	e.fake.SetPRFiles("dir/gone file.go", "x\ty.go", "images b/logo.png", "a", "z b/a b/z", "bin.dat")

	run := e.seedRun(t, repo, 32, "sha-e", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-edge", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	// 清单外的伪造路径 → 40004。
	for _, badFile := range []string{"fake.go", "a b/z"} {
		bad := `{"findings":[{"level":"P1","file":"` + badFile + `","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
		err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(bad))
		if err == nil || !strings.Contains(err.Error(), "核对审查对象") {
			t.Fatalf("fake path %q = %v, want 40004 核对审查对象", badFile, err)
		}
	}
	// 清单内路径（含特殊字符、rename 两侧、二进制）均受理。
	good := `{"findings":[{"level":"P1","file":"dir/gone file.go","body":"不该删除"},{"level":"P2","file":"x\ty.go","body":"含 tab 的删除文件"},{"level":"P2","file":"images b/logo.png","body":"路径含 b/ 的删除文件"},{"level":"P2","file":"z b/a b/z","body":"重命名新路径"},{"level":"P2","file":"bin.dat","body":"二进制文件删除影响下载"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)); err != nil {
		t.Fatalf("deleted/renamed list paths must be accepted: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCompleted {
		t.Fatalf("status = %q, want completed", after.Status)
	}
}

// TestAgentSubmitResultConcurrentReplayAtHeadRecheck pr2 复核窗口的并发
// 完全相同重放（r4 Note）：文件校验通过后、事务前并发提交已完成——GetPullRequest
// 复核失败或 head 前进两个出口都要先复核幂等成功，不报 50200/40920。
func TestAgentSubmitResultConcurrentReplayAtHeadRecheck(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 33, &githubapi.PullRequest{
		RepoFullName: repo, Number: 33, HeadSHA: "sha-r",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 33, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-r", RoundNumber: 1, UpdatedAt: now,
	})
	e.fake.SetTreeFiles(repo, "sha-r", "a.go")

	body := `{"findings":[{"level":"P1","file":"a.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	expected, err := json.Marshal(&api.ReviewCodeResult{
		Findings: []api.ReviewCodeFinding{{Level: api.P1, File: "a.go", Body: "x"}},
		Prior:    []api.ReviewPriorDecision{},
		Summary:  "s", UiChanged: false, UiChangedSinceLastVisual: false,
	})
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	complete := func(runID string) {
		e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", runID).
			Updates(map[string]any{"status": model.ReviewRunCompleted, "result_json": string(expected)})
	}

	// pr2 复核拉取失败分支：第 2 次 GetPullRequest 前并发完成 + 注入错误。
	run1 := e.seedRun(t, repo, 33, "sha-r", model.ReviewRunRunning, now)
	calls1 := 0
	e.fake.SetPRHook(func(int) {
		calls1++
		if calls1 == 2 {
			complete(run1.ID)
			e.fake.SetPRError(repo, errors.New("github 5xx"))
		}
	})
	if err := e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(body)); err != nil {
		t.Fatalf("pr2-error exit must honor concurrent replay: %v", err)
	}
	e.fake.SetPRHook(nil)
	e.fake.SetPRError(repo, nil)

	// pr2 head 前进分支：第 2 次 GetPullRequest 前并发完成 + head 变了。
	run2 := e.seedRun(t, repo, 33, "sha-r", model.ReviewRunRunning, now)
	calls2 := 0
	e.fake.SetPRHook(func(int) {
		calls2++
		if calls2 == 2 {
			complete(run2.ID)
			e.fake.SetPR(repo, 33, &githubapi.PullRequest{
				RepoFullName: repo, Number: 33, HeadSHA: "sha-new",
				Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
			})
		}
	})
	if err := e.review.AgentSubmitResult(context.Background(), run2.ID, []byte(body)); err != nil {
		t.Fatalf("pr2-supersede exit must honor concurrent replay: %v", err)
	}
	e.fake.SetPRHook(nil)

	// 首个 supersede 出口（r5 Note）：首次 GetPullRequest 前并发完成 +
	// head 已变——不先复核就会把合法重放顶成 40920。
	run3 := e.seedRun(t, repo, 33, "sha-r", model.ReviewRunRunning, now)
	calls3 := 0
	e.fake.SetPRHook(func(int) {
		calls3++
		if calls3 == 1 {
			complete(run3.ID)
			e.fake.SetPR(repo, 33, &githubapi.PullRequest{
				RepoFullName: repo, Number: 33, HeadSHA: "sha-new",
				Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
			})
		}
	})
	if err := e.review.AgentSubmitResult(context.Background(), run3.ID, []byte(body)); err != nil {
		t.Fatalf("first supersede exit must honor concurrent replay: %v", err)
	}
}

// TestAgentSubmitResultFileCheckBudget 唯一路径数超预算（64）→ 40004，
// 不做逐个 GitHub 查询的失控扇出。
func TestAgentSubmitResultFileCheckBudget(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 24, &githubapi.PullRequest{
		RepoFullName: repo, Number: 24, HeadSHA: "sha-b",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 24, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-b", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 24, "sha-b", model.ReviewRunRunning, now)

	var sb strings.Builder
	sb.WriteString(`{"findings":[`)
	for i := 0; i < findingFileCheckLimit+1; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"level":"P2","file":"dir%d/f.go","body":"x"}`, i)
	}
	sb.WriteString(`],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`)
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(sb.String()))
	assertErrCode(t, err, 40004)
	if !strings.Contains(err.Error(), "预算") {
		t.Fatalf("error = %v, want 预算 hint", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, budget rejection must not change run", after.Status)
	}
}

// TestAgentSubmitResultConcurrentReplayOnCheckError 并发完全相同重放窗口：
// 加载时运行还在途，文件校验期间另一提交已完成且 canonical 相同——校验
// 错误（GitHub 5xx）不得把合法重放顶成 502，复核后按幂等成功返回。
func TestAgentSubmitResultConcurrentReplayOnCheckError(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 25, &githubapi.PullRequest{
		RepoFullName: repo, Number: 25, HeadSHA: "sha-c",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 25, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-c", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 25, "sha-c", model.ReviewRunRunning, now)

	body := `{"findings":[{"level":"P1","file":"a.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	// canonical 与 AgentSubmitResult 内部 Marshal 的已校验结构逐字节一致。
	expected, err := json.Marshal(&api.ReviewCodeResult{
		Findings: []api.ReviewCodeFinding{{Level: api.P1, File: "a.go", Body: "x"}},
		Prior:    []api.ReviewPriorDecision{},
		Summary:  "s", UiChanged: false, UiChangedSinceLastVisual: false,
	})
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	// 钩子模拟并发提交在树查询前完成本运行；随后查询注定失败。
	e.fake.SetTreeHook(func(_, _, _ string) {
		e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"status": model.ReviewRunCompleted, "result_json": string(expected)})
	})
	e.fake.SetTreeError(repo, errors.New("github 5xx"))

	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body)); err != nil {
		t.Fatalf("identical concurrent replay must succeed, got %v", err)
	}
}

// TestAgentSubmitResultVisualSkipsFileCheck 视觉运行的 findings 关联截图
// label 而非仓库路径，不查文件归属。
func TestAgentSubmitResultVisualSkipsFileCheck(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 21, &githubapi.PullRequest{
		RepoFullName: repo, Number: 21, HeadSHA: "sha-v",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 21, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-v", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 21, "sha-v", model.ReviewRunRunning, now)
	run.Kind = model.ReviewKindVisual
	if err := e.svc.db.Save(run).Error; err != nil {
		t.Fatalf("save visual run: %v", err)
	}
	// 空索引：若误走到文件检查必 40004。
	e.fake.SetTreeFiles(repo, "sha-v")

	body := `{"summary":"视觉结论","findings":[{"severity":"minor","screenshot":"s1","dimension":"spacing","body":"b","fix":"f"}],"prior":[],"unmeasurable":[]}`
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body)); err != nil {
		t.Fatalf("visual submit must skip file check: %v", err)
	}
}

// TestResolveRunTokenIdentityThreadID 令牌身份带线程 ID（INT-169 错配拒绝
// 的日志需要定位线程）。
func TestResolveRunTokenIdentityThreadID(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 22, "sha-k", model.ReviewRunRunning, now)
	raw := "fsr_" + strings.Repeat("b", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	id, err := e.review.ResolveRunToken(context.Background(), raw, true, run.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id.ThreadID != run.ThreadID {
		t.Fatalf("identity.ThreadID = %q, want %q", id.ThreadID, run.ThreadID)
	}
}

// TestResolveRunTokenMismatchZeroSideEffects INT-169 核心：dispatched 运行的
// 令牌带错配的 URL runId 解析 → 40307，且 dispatched→running 推进、
// dispatched_at/started_at 回填、令牌消费全部不得发生——错配请求对双方
// 运行零副作用；随后同一令牌的正确解析仍正常推进。
func TestResolveRunTokenMismatchZeroSideEffects(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 23, "sha-m", model.ReviewRunDispatched, now)
	raw := "fsr_" + strings.Repeat("c", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	// B 的令牌打 A 的 URL：拒绝且 run 行完全不动。
	if _, err := e.review.ResolveRunToken(context.Background(), raw, true, "run-other"); err != errs.ErrReviewRunMismatch {
		t.Fatalf("mismatched resolve err = %v, want ErrReviewRunMismatch", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunDispatched || after.StartedAt != nil {
		t.Fatalf("status=%q started_at=%v, mismatch must not advance run", after.Status, after.StartedAt)
	}
	var tok model.ReviewRunToken
	e.svc.db.First(&tok, "run_id = ?", run.ID)
	if tok.ConsumedAt != nil {
		t.Fatal("mismatch must not consume token")
	}

	// 同一令牌对正确 runId 正常通过并推进 running（含 dispatched_at 回填）。
	id, err := e.review.ResolveRunToken(context.Background(), raw, true, run.ID)
	if err != nil || id.RunID != run.ID {
		t.Fatalf("matched resolve = %v %+v", err, id)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunRunning || after.DispatchedAt == nil {
		t.Fatalf("status=%q dispatched_at=%v, want running + backfilled", after.Status, after.DispatchedAt)
	}
}

// TestResolveRunTokenMismatchTerminalConsumed requireLive=false（result 端点）
// 且令牌已消费、运行已终态：错配仍须 40307 先于终态重放判定——终态运行
// 令牌也不能打别的运行。
func TestResolveRunTokenMismatchTerminalConsumed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 26, "sha-t", model.ReviewRunCompleted, now)
	consumed := now.Add(-time.Minute)
	raw := "fsr_" + strings.Repeat("d", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		ConsumedAt: &consumed,
	})
	if _, err := e.review.ResolveRunToken(context.Background(), raw, false, "run-other"); err != errs.ErrReviewRunMismatch {
		t.Fatalf("terminal+consumed mismatch = %v, want ErrReviewRunMismatch", err)
	}
}

// failOnceResolver 首次 ClientFor 注入失败的解析器包装：模拟并发完全相同
// 提交在客户端解析窗口完成的场景。
type failOnceResolver struct {
	inner  forge.ReviewClientResolver
	failed bool
	onFail func()
}

func (f *failOnceResolver) ClientFor(ctx context.Context, integrationID, repoFullName string, installationID int64) (forge.ReviewClient, error) {
	if !f.failed {
		f.failed = true
		f.onFail()
		return nil, errors.New("resolver down")
	}
	return f.inner.ClientFor(ctx, integrationID, repoFullName, installationID)
}

// TestAgentSubmitResultConcurrentReplayAtClientResolve client 解析失败出口
// （r6 Note）：并发完全相同提交在 reviewClient 调用前完成——解析失败不得
// 把合法重放顶成 502，复核后按幂等成功返回。
func TestAgentSubmitResultConcurrentReplayAtClientResolve(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 34, &githubapi.PullRequest{
		RepoFullName: repo, Number: 34, HeadSHA: "sha-c",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 34, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-c", RoundNumber: 1, UpdatedAt: now,
	})
	e.fake.SetTreeFiles(repo, "sha-c", "a.go")
	run := e.seedRun(t, repo, 34, "sha-c", model.ReviewRunRunning, now)

	body := `{"findings":[{"level":"P1","file":"a.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	expected, err := json.Marshal(&api.ReviewCodeResult{
		Findings: []api.ReviewCodeFinding{{Level: api.P1, File: "a.go", Body: "x"}},
		Prior:    []api.ReviewPriorDecision{},
		Summary:  "s", UiChanged: false, UiChangedSinceLastVisual: false,
	})
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	e.review.clients = &failOnceResolver{inner: e.review.clients, onFail: func() {
		e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"status": model.ReviewRunCompleted, "result_json": string(expected)})
	}}

	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(body)); err != nil {
		t.Fatalf("client-resolve exit must honor concurrent replay: %v", err)
	}
}

// TestAgentDiffOversizedFallsBackToTruncated diff 超本端读入上限时端点给
// 截断件（前缀可能为空）而非 50200（r6 Note）——超巨型 PR 每次拉取失败
// 会把运行卡到 deadline；未超限 diff 保持原口径。
func TestAgentDiffOversizedFallsBackToTruncated(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 35, "sha-d", model.ReviewRunRunning, now)

	// 未超限 diff 原样返回。
	e.fake.SetDiff("diff --git a/a.go b/a.go\n")
	out, err := e.review.AgentDiff(context.Background(), run.ID)
	if err != nil || out.Truncated {
		t.Fatalf("small diff = %+v, %v", out, err)
	}

	// >1MB → 截断标记。
	e.fake.SetDiff(strings.Repeat("x", reviewDiffMaxBytes+10))
	out, err = e.review.AgentDiff(context.Background(), run.ID)
	if err != nil || !out.Truncated || len(out.Content) != reviewDiffMaxBytes {
		t.Fatalf(">1MB diff = truncated=%v len=%d, %v", out.Truncated, len(out.Content), err)
	}

	// 超本端读入上限 → 截断件兜底（前缀为空也返回 truncated:true），不 50200。
	e.fake.SetDiffError(githubapi.ErrBodyTooLarge)
	out, err = e.review.AgentDiff(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("oversized diff must not 50200: %v", err)
	}
	if !out.Truncated || out.Content != "" {
		t.Fatalf("oversized diff = truncated=%v content=%q", out.Truncated, out.Content)
	}
}

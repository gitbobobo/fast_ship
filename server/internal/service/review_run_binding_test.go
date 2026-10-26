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

// TestAgentSubmitResultDeletedFileFindings head 树未命中的路径用本轮
// diff 兜底：PR 删除/重命名走的文件仍是合法审查对象（r2 P2）；
// 既不在树也不在 diff 的路径照旧 40004。
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
	// 树里只有 a.go；diff 记录 gone.go 被删除、old.go→new.go 重命名。
	e.fake.SetTreeFiles(repo, "sha-d", "a.go", "new.go")
	e.fake.SetDiff("diff --git a/gone.go b/gone.go\ndeleted file mode 100644\n" +
		"--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n" +
		"diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n")

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

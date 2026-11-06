package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// INT-204 平台侧改进的单元测试：状态只因 P0/P1 判红（prior 处置不计入）、
// 提示词按增量/超限模式拼装、超限评论只列阻断问题、行内跳过 P2、派发侧的
// 增量基线与轮次上限兜底。

// int204Run 组一条 completed 的代码运行；findings/prior 由 JSON 直填。
func int204Run(t *testing.T, findings, prior string) model.ReviewRun {
	t.Helper()
	result, err := json.Marshal(map[string]any{
		"summary": "s", "findings": json.RawMessage(findings),
		"prior": json.RawMessage(prior), "ui_changed": false,
		"ui_changed_since_last_visual": false,
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return model.ReviewRun{
		Kind: model.ReviewKindCode, Status: model.ReviewRunCompleted,
		ResultJSON: string(result), InstanceID: "claude", Model: "m", EnvironmentID: "e",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

// TestSummarizeCodeRunsP0P1OnlyBlocking INT-204 状态口径：只有 P0/P1 判红；
// 只有 P2 时 success 且描述带条数；prior 条目（含 rejected 的 P1）不计入。
func TestSummarizeCodeRunsP0P1OnlyBlocking(t *testing.T) {
	// 只有 P2 → success，描述含条数。
	st, desc := summarizeCodeRuns([]model.ReviewRun{int204Run(t,
		`[{"level":"P2","file":"a.go","line":1,"body":"边界"}]`, `[]`)}, false)
	if st != "success" || !strings.Contains(desc, "P2 建议 1 条") {
		t.Fatalf("P2-only: state=%q desc=%q, want success + 条数", st, desc)
	}

	// 含 P1（配 P2/Note）→ failure，描述按级别计数。
	st, desc = summarizeCodeRuns([]model.ReviewRun{int204Run(t,
		`[{"level":"P1","file":"a.go","line":1,"body":"bug"},{"level":"P2","file":"b.go","body":"边界"},{"level":"Note","file":"c.go","body":"nit"}]`, `[]`)}, false)
	if st != "failure" || !strings.Contains(desc, "P1 1 条") || !strings.Contains(desc, "P2 1 条") || strings.Contains(desc, "Note") {
		t.Fatalf("P1: state=%q desc=%q", st, desc)
	}

	// 含 P0 → failure。
	st, _ = summarizeCodeRuns([]model.ReviewRun{int204Run(t,
		`[{"level":"P0","file":"a.go","line":1,"body":"崩溃"}]`, `[]`)}, false)
	if st != "failure" {
		t.Fatalf("P0: state=%q, want failure", st)
	}

	// 超限口径（审查 R2）：描述只列 P0/P1——failure 不再带 P2 计数，
	// P2-only 直接「审查通过」不带建议数，与超限评论的红绿口径一致。
	st, desc = summarizeCodeRuns([]model.ReviewRun{int204Run(t,
		`[{"level":"P1","file":"a.go","line":1,"body":"bug"},{"level":"P2","file":"b.go","body":"边界"}]`, `[]`)}, true)
	if st != "failure" || !strings.Contains(desc, "P1 1 条") || strings.Contains(desc, "P2") {
		t.Fatalf("超限 failure 描述应只列 P0/P1: state=%q desc=%q", st, desc)
	}
	st, desc = summarizeCodeRuns([]model.ReviewRun{int204Run(t,
		`[{"level":"P2","file":"a.go","line":1,"body":"边界"}]`, `[]`)}, true)
	if st != "success" || desc != "审查通过" {
		t.Fatalf("超限 P2-only 应为纯「审查通过」: state=%q desc=%q", st, desc)
	}

	// prior.rejected（P1 级别、带 reason）不是本轮新问题：不计入状态。
	st, desc = summarizeCodeRuns([]model.ReviewRun{int204Run(t, `[]`,
		`[{"level":"P1","file":"a.go","line":1,"body":"原发现","status":"rejected","reason":"有意为之"}]`)}, false)
	if st != "success" || !strings.Contains(desc, "审查通过") {
		t.Fatalf("prior rejected 不计入状态: state=%q desc=%q", st, desc)
	}

	// prior.open 的 P1 是遗留阻断（审查 R5）：增量轮把它只标 prior(open)
	// 而不写 findings 时，状态照样判红——已知阻断问题不能从所有面消失。
	st, desc = summarizeCodeRuns([]model.ReviewRun{int204Run(t, `[]`,
		`[{"level":"P1","file":"a.go","line":1,"body":"仍存在","status":"open"}]`)}, false)
	if st != "failure" || !strings.Contains(desc, "遗留未解决 1 条") {
		t.Fatalf("prior open P1 应判红: state=%q desc=%q", st, desc)
	}

	// prior.open 的 P2 是非阻断建议，不计入状态。
	st, _ = summarizeCodeRuns([]model.ReviewRun{int204Run(t, `[]`,
		`[{"level":"P2","file":"a.go","body":"边界","status":"open"}]`)}, false)
	if st != "success" {
		t.Fatalf("prior open P2 不应判红: state=%q", st)
	}

	// prior.open 级别缺失按阻断（对齐视觉侧原则：模型标了未解决却没写级别
	// 时不能变绿）。
	st, _ = summarizeCodeRuns([]model.ReviewRun{int204Run(t, `[]`,
		`[{"body":"没写级别","status":"open"}]`)}, false)
	if st != "failure" {
		t.Fatalf("prior open 无级别应判红: state=%q", st)
	}

	// 多组合合并：一个组合 P1、另一个组合只有 P2 → failure，两个级别都列出。
	st, desc = summarizeCodeRuns([]model.ReviewRun{
		int204Run(t, `[{"level":"P1","file":"a.go","line":1,"body":"bug"}]`, `[]`),
		int204Run(t, `[{"level":"P2","file":"b.go","body":"边界"}]`, `[]`),
	}, false)
	if st != "failure" || !strings.Contains(desc, "P1 1 条") || !strings.Contains(desc, "P2 1 条") {
		t.Fatalf("多组合: state=%q desc=%q", st, desc)
	}
}

// TestRenderReviewPromptIncrementalAndRoundCap INT-204 提示词按模式拼装：
// 全量模式无增量节与 prev 占位符；增量模式含上一轮受审 SHA 的 diff 范围与
// prior 处置要求；超限轮追加 P0/P1-only 指令，未超限不含。
func TestRenderReviewPromptIncrementalAndRoundCap(t *testing.T) {
	mkRun := func(round int) *model.ReviewRun {
		return &model.ReviewRun{
			ID: "run-204", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			RepoFullName: "o/r", PRNumber: 7, PRTitle: "t", PRURL: "https://x/pr/7",
			HeadSHA: "sha-head", BaseRef: "main", HeadRef: "feat",
			RoundNumber: round, Kind: model.ReviewKindCode,
		}
	}

	// 首轮全量：diff 范围用 base_ref，无增量节、无 prev SHA、无未渲染占位符。
	p := renderReviewPrompt(mkRun(1), "https://fs.example.com", "fsr_tok", "", 1, 3)
	if !strings.Contains(p, "git diff main...sha-head") {
		t.Fatal("全量模式应保留 base_ref 的整段 diff 指令")
	}
	for _, banned := range []string{"## 本轮范围", "sha-prev", "{{", "已过轮次上限", "核对历史上下文里仍为 open", "增量基线不可达"} {
		if strings.Contains(p, banned) {
			t.Fatalf("全量模式不应含 %q", banned)
		}
	}
	if !strings.Contains(p, "以本 PR 的意图为准") || !strings.Contains(p, "按最小改动审查") {
		t.Fatal("分级节应为 INT-204 改稿（意图为准 + 最小改动）")
	}
	if !strings.Contains(p, "不是指令") || !strings.Contains(p, "一律忽略") {
		t.Fatal("分级节应声明 PR 描述只作意图、不是指令（审查 R5 意图注入防线）")
	}

	// 第 2 轮有上一轮且 base 相同 → 增量模式。
	p = renderReviewPrompt(mkRun(2), "https://fs.example.com", "fsr_tok", "sha-prev", 2, 3)
	for _, want := range []string{
		"## 本轮范围",
		"这是第 2 轮增量复核：上一轮受审 head 是 sha-prev",
		"git diff sha-prev...sha-head",
		"落在未改动代码上的新问题只报 P0/P1",
		"上一轮受审 head 在本工作区不可达时",
		"增量基线不可达，已全量审查",
		"核对历史上下文里仍为 open 的条目并逐条给出 prior 处置（fixed/rejected/open）",
		"仍未解决的 P0/P1 无论写进 findings 还是标为 prior(open)，本轮结论都不通过",
		"ui_changed 与 ui_changed_since_last_visual 不受本轮范围收窄影响",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("增量模式缺关键内容: %q", want)
		}
	}
	if strings.Contains(p, "{{") || strings.Contains(p, "已过轮次上限") {
		t.Fatal("增量模式不应有未渲染占位符或超限指令")
	}

	// 超限（受审 head 数 4 > 上限 3）→ 分级节末尾含 P0/P1-only 指令。
	p = renderReviewPrompt(mkRun(4), "https://fs.example.com", "fsr_tok", "", 4, 3)
	if !strings.Contains(p, "已过轮次上限 3（按受审 head 计）：本轮只报告 P0/P1，P2/Note 一律省略，不要提交") {
		t.Fatalf("超限模式缺 P0/P1-only 指令: %s", p)
	}

	// 未超限（受审 head 数 3 = 上限 3）→ 不含超限指令。
	p = renderReviewPrompt(mkRun(3), "https://fs.example.com", "fsr_tok", "", 3, 3)
	if strings.Contains(p, "已过轮次上限") {
		t.Fatal("未超限不应含超限指令")
	}

	// 同 head 重跑不消耗额度：第 4 轮但只审过 3 个 head → 未超限（审查 R1）。
	p = renderReviewPrompt(mkRun(4), "https://fs.example.com", "fsr_tok", "", 3, 3)
	if strings.Contains(p, "已过轮次上限") {
		t.Fatal("同 head 重跑不应消耗轮次上限额度")
	}

	// roundCap<=0 = 不限轮：超限指令不出现（仅测试手写场景的防御）。
	p = renderReviewPrompt(mkRun(4), "https://fs.example.com", "fsr_tok", "", 4, 0)
	if strings.Contains(p, "已过轮次上限") {
		t.Fatal("roundCap<=0 应视为不限轮")
	}
}

// TestPrevReviewedHeadSHAAndRoundCap 派发侧的两个兜底查询（INT-204）：
// 上一轮受审 head 在 base 变化/无上一轮/首轮时回退全量；轮次上限无行或 0
// 按默认 3 归一。
func TestPrevReviewedHeadSHAAndRoundCap(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	ctx := context.Background()

	r1 := e.seedRun(t, repo, 30, "sha-1", model.ReviewRunCompleted, now)
	e.svc.db.Model(r1).Updates(map[string]any{"base_ref": "main", "dispatched_at": now})
	r2 := e.seedRun(t, repo, 30, "sha-2", model.ReviewRunCompleted, now)
	e.svc.db.Model(r2).Updates(map[string]any{"base_ref": "main", "round_number": 2, "dispatched_at": now})

	// 上一轮存在且 base 一致 → 增量基线。
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "sha-1" {
		t.Fatalf("prev = %q, want sha-1", got)
	}

	// 多组合项目里上一轮部分组合未完成（审查 R2）：any 语义会漏掉未完成
	// 组合对历史代码的覆盖 → 回退全量；全部 completed 后恢复增量。
	r1b := e.seedRun(t, repo, 30, "sha-1", model.ReviewRunTimedOut, now)
	e.svc.db.Model(r1b).Updates(map[string]any{"base_ref": "main", "instance_id": "o4-mini"})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("上一轮部分组合未完成应回退全量, prev = %q", got)
	}
	e.svc.db.Model(r1b).Updates(map[string]any{"status": model.ReviewRunCompleted})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "sha-1" {
		t.Fatalf("上一轮全部 completed 后应恢复增量, prev = %q", got)
	}
	// base 变化 → 回退全量。
	e.svc.db.Model(r2).Updates(map[string]any{"base_ref": "develop"})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("base 变化应回退全量, prev = %q", got)
	}
	// base 仅大小写不同也是不同分支（git 分支名大小写敏感）→ 回退全量。
	e.svc.db.Model(r2).Updates(map[string]any{"base_ref": "Main"})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("base 大小写不同应回退全量, prev = %q", got)
	}
	// 首轮（round-1 = 0）→ 空串。
	r2Round1 := *r2
	r2Round1.RoundNumber = 1
	if got := e.review.prevReviewedHeadSHA(ctx, &r2Round1); got != "" {
		t.Fatalf("首轮应回退全量, prev = %q", got)
	}

	// 上一轮从未审完（快推被 supersede 取消/失败）→ 其改动从未被审，增量
	// 会漏掉它们 → 回退全量（审查 R1）。
	e.svc.db.Model(r1).Updates(map[string]any{"status": model.ReviewRunCancelled})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("上一轮无 completed 运行应回退全量, prev = %q", got)
	}
	e.svc.db.Model(r1).Updates(map[string]any{"status": model.ReviewRunCompleted})

	// manual/comment 同 head 重跑（上一轮 head == 本轮 head）→ diff X...X 为空，
	// 重审必须全量（审查 R1）。
	e.svc.db.Model(r2).Updates(map[string]any{"head_sha": "sha-1"})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("同 head 重跑应回退全量, prev = %q", got)
	}
	e.svc.db.Model(r2).Updates(map[string]any{"head_sha": "sha-2"})

	// 本轮组合在上一轮没有同组合的 completed 运行（审查 R3：项目中途新增
	// 组合）→ 回退全量；恢复已知组合后增量照常。先恢复 base（大小写用例
	// 把它改成了 Main）。
	e.svc.db.Model(r2).Updates(map[string]any{"base_ref": "main"})
	e.svc.db.Model(r2).Updates(map[string]any{"instance_id": "brand-new"})
	if got := e.review.prevReviewedHeadSHA(ctx, r2); got != "" {
		t.Fatalf("新组合无上一轮覆盖应回退全量, prev = %q", got)
	}
	e.svc.db.Model(r2).Updates(map[string]any{"instance_id": "claude"})

	// reviewedHeadCount：同 head 重跑不重复计（sha-1 两轮 + sha-2 一轮 → 2）。
	// 历史轮只计派发过的 head；当前轮按运行行存在即计（审查 R3：首个组合
	// 渲染提示词时本轮还没派发，同轮组合与同步侧才不会拿到不同计数）。
	r3 := e.seedRun(t, repo, 30, "sha-2", model.ReviewRunFailed, now)
	e.svc.db.Model(r3).Updates(map[string]any{"base_ref": "main", "round_number": 3, "dispatched_at": now})
	if got := e.review.reviewedHeadCount(e.project.ID, repo, 30, r2.IntegrationID, 3); got != 2 {
		t.Fatalf("reviewedHeadCount = %d, want 2（同 head 重跑不重复计）", got)
	}
	r4 := e.seedRun(t, repo, 30, "sha-3", model.ReviewRunCancelled, now)
	e.svc.db.Model(r4).Updates(map[string]any{"base_ref": "main", "round_number": 4})
	if got := e.review.reviewedHeadCount(e.project.ID, repo, 30, r2.IntegrationID, 4); got != 3 {
		t.Fatalf("当前轮 head 按运行行存在即计（派发前也计）: got %d, want 3", got)
	}
	// 历史轮（round < upTo）从未派发就被 supersede 的 head 不消耗额度。
	r5 := e.seedRun(t, repo, 30, "sha-4", model.ReviewRunCancelled, now)
	e.svc.db.Model(r5).Updates(map[string]any{"base_ref": "main", "round_number": 3})
	if got := e.review.reviewedHeadCount(e.project.ID, repo, 30, r2.IntegrationID, 4); got != 3 {
		t.Fatalf("历史轮从未派发的 head 不应计入: got %d, want 3", got)
	}
	e.svc.db.Model(r5).Updates(map[string]any{"dispatched_at": now})
	if got := e.review.reviewedHeadCount(e.project.ID, repo, 30, r2.IntegrationID, 4); got != 4 {
		t.Fatalf("历史轮派发过的 head 应计入: got %d, want 4", got)
	}

	// 轮次上限：有行取行值；0 归一为 3；无行默认 3。
	setRow := func(cap int) {
		t.Helper()
		e.svc.db.Where("project_id = ?", e.project.ID).
			Delete(&model.ProjectReviewSettings{})
		e.enableReview(t, func(s *model.ProjectReviewSettings) { s.RoundCap = cap })
	}
	setRow(7)
	if got := e.review.reviewRoundCap(e.project.ID); got != 7 {
		t.Fatalf("roundCap = %d, want 7", got)
	}
	setRow(0)
	if got := e.review.reviewRoundCap(e.project.ID); got != 3 {
		t.Fatalf("roundCap 0 应归一 3, got %d", got)
	}
	e.svc.db.Where("project_id = ?", e.project.ID).Delete(&model.ProjectReviewSettings{})
	if got := e.review.reviewRoundCap(e.project.ID); got != 3 {
		t.Fatalf("无设置行应默认 3, got %d", got)
	}
}

// TestBuildCodeCommentOverCap INT-204 超限评论口径：结论与计数只看 P0/P1，
// P2/Note 不渲染，表格下注明超限；未超限行为不变。
func TestBuildCodeCommentOverCap(t *testing.T) {
	mkRuns := func(t *testing.T) []model.ReviewRun {
		return []model.ReviewRun{int204Run(t,
			`[{"level":"P0","file":"a.go","line":1,"body":"崩溃"},
			  {"level":"P1","file":"b.go","line":2,"body":"bug"},
			  {"level":"P2","file":"c.go","line":3,"body":"边界"},
			  {"level":"Note","file":"d.go","body":"nit"}]`, `[]`)}
	}

	// 超限（受审 head 数 4 > 上限 3，第 4 轮）：P2/Note 不出现，表格下有注明行。
	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890",
		links: githubLinks("https://github.com"), RoundNumber: 4, RoundCap: 3, ReviewedHeads: 4}
	body := buildCodeComment(meta, mkRuns(t), nil, false, false)
	if !strings.Contains(body, "已过轮次上限 3（按受审 head 计），仅报告阻断问题。") {
		t.Fatalf("超限评论缺注明行: %s", body)
	}
	if !strings.Contains(body, "发现 P0 1 条、P1 1 条。") {
		t.Fatalf("超限结论应只列 P0/P1: %s", body)
	}
	if strings.Contains(body, "**P2**") || strings.Contains(body, "<details>") || strings.Contains(body, "**Note**") {
		t.Fatal("超限评论不应渲染 P2/Note")
	}
	if !strings.Contains(body, "**P0**") || !strings.Contains(body, "**P1**") {
		t.Fatal("超限评论 P0/P1 照常渲染")
	}

	// 超限且只有 P2：结论无阻断问题；P2/Note 计数列也省略（–），连建议数
	// 都不出现在评论里（契约「超限后只报告 P0/P1」）。
	onlyP2 := []model.ReviewRun{int204Run(t,
		`[{"level":"P2","file":"c.go","line":3,"body":"边界"},{"level":"P2","file":"e.go","body":"边界2"}]`, `[]`)}
	body = buildCodeComment(meta, onlyP2, nil, false, false)
	if !strings.Contains(body, "无阻断问题。") || strings.Contains(body, "另有 P2 建议") {
		t.Fatalf("超限仅 P2 结论不符: %s", body)
	}
	if strings.Contains(body, "**P2**") {
		t.Fatal("超限仅 P2 时分节不应渲染 P2")
	}
	if !strings.Contains(body, "| 0 | 0 | – | – |") || strings.Contains(body, "| 0 | 0 | 2 | 0 |") {
		t.Fatalf("超限总览表 P2/Note 计数列应省略: %s", body)
	}

	// 超限且全净：通过文案换成「阻断问题」措辞。
	clean := []model.ReviewRun{int204Run(t, `[]`, `[]`)}
	body = buildCodeComment(meta, clean, nil, false, false)
	if !strings.Contains(body, "全部组合通过，未发现阻断问题。") {
		t.Fatalf("超限全净结论不符: %s", body)
	}

	// 未超限（RoundCap 未填 = 不限）：P0–P2 与 Note 折叠照常。
	meta.RoundCap = 0
	body = buildCodeComment(meta, mkRuns(t), nil, false, false)
	if strings.Contains(body, "已过轮次上限") {
		t.Fatal("未超限不应有注明行")
	}
	if !strings.Contains(body, "**P2**") || !strings.Contains(body, "<details>") {
		t.Fatal("未超限 P2/Note 照常渲染")
	}
	if !strings.Contains(body, "发现 P0 1 条、P1 1 条、P2 1 条。") {
		t.Fatalf("未超限结论按 P0–P2 计数: %s", body)
	}

	// 未超限且只有 P2（INT-204 一致性）：状态已是 success，结论措辞与之一致，
	// 不写「发现」制造红感。
	body = buildCodeComment(meta, onlyP2, nil, false, false)
	if !strings.Contains(body, "无阻断问题，另有 P2 建议 2 条。") {
		t.Fatalf("未超限仅 P2 结论不符: %s", body)
	}
	if strings.Contains(body, "发现 P2") {
		t.Fatalf("未超限仅 P2 不应用「发现」措辞: %s", body)
	}

	// prior(open) 渲染与计数（审查 R5）：未超限全列（非阻断带标记），
	// 遗留阻断进结论；超限轮只留阻断级别，非阻断遗留不出现。
	withPrior := []model.ReviewRun{int204Run(t,
		`[{"level":"P1","file":"a.go","line":1,"body":"新bug"}]`,
		`[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"},
		  {"level":"P2","file":"y.go","body":"遗留P2","status":"open"},
		  {"level":"P1","file":"z.go","body":"已驳回","status":"rejected","reason":"有意为之"}]`)}
	meta.RoundCap = 3
	meta.ReviewedHeads = 1
	body = buildCodeComment(meta, withPrior, nil, false, false)
	if !strings.Contains(body, "**仍未解决**") || !strings.Contains(body, "遗留P1") ||
		!strings.Contains(body, "遗留P2") || !strings.Contains(body, "（非阻断）") {
		t.Fatalf("未超限应全列 open prior（非阻断带标记）: %s", body)
	}
	if !strings.Contains(body, "发现 P1 1 条、遗留未解决 1 条。") {
		t.Fatalf("结论应含遗留阻断计数: %s", body)
	}
	if strings.Contains(body, "已驳回") {
		t.Fatalf("rejected 不应渲染: %s", body)
	}
	meta.RoundNumber = 4
	meta.ReviewedHeads = 4
	body = buildCodeComment(meta, withPrior, nil, false, false)
	if strings.Contains(body, "遗留P2") || strings.Contains(body, "（非阻断）") {
		t.Fatalf("超限轮非阻断遗留应省略: %s", body)
	}
	if !strings.Contains(body, "遗留P1") || !strings.Contains(body, "遗留未解决 1 条") {
		t.Fatalf("超限轮阻断遗留应照常: %s", body)
	}
}

// TestPostInlineReviewsOverCapSkipsP2 INT-204 超限行内：P2 与 Note 一样跳过，
// P0/P1 照常提交。
func TestPostInlineReviewsOverCapSkipsP2(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 21, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cap", RoundNumber: 4,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	run := e.seedMixedFindingsRun(t, repo, 21, "sha-cap", now)

	client, err := e.review.reviewClient(context.Background(), model.ForgeIntegrationBuiltinGitHubID, repo, 1)
	if err != nil {
		t.Fatalf("reviewClient: %v", err)
	}
	lines := map[string]map[int]bool{"a": {1: true, 2: true, 3: true, 4: true}}
	// 超限（受审 head 数 4 > 上限 3）：只提交 P0/P1 行内。
	if err := e.review.postInlineReviews(context.Background(), client, ledger,
		[]model.ReviewRun{*run}, lines, 1, now, true); err != nil {
		t.Fatalf("postInlineReviews: %v", err)
	}
	inputs := e.fake.ReviewInputs()
	if len(inputs) != 1 {
		t.Fatalf("review 数 = %d, want 1", len(inputs))
	}
	var bodies []string
	for _, c := range inputs[0].Comments {
		bodies = append(bodies, c.Body)
	}
	joined := strings.Join(bodies, "|")
	if len(bodies) != 2 || !strings.Contains(joined, "P0：") || !strings.Contains(joined, "P1：") {
		t.Fatalf("超限行内应只有 P0/P1: %v", bodies)
	}
	if strings.Contains(joined, "P2：") || strings.Contains(joined, "Note：") {
		t.Fatalf("超限行内不应含 P2/Note: %v", bodies)
	}

	// 未超限（overCap=false）：换一条新运行（运行 id 不重复，避免上一条
	// review 的对账认领吞掉本次提交），P2 行内照常，Note 仍跳过。
	run2 := e.seedMixedFindingsRun(t, repo, 21, "sha-cap", now)
	if err := e.review.postInlineReviews(context.Background(), client, ledger,
		[]model.ReviewRun{*run2}, lines, 1, now, false); err != nil {
		t.Fatalf("postInlineReviews(不限): %v", err)
	}
	inputs = e.fake.ReviewInputs()
	if len(inputs) != 2 || len(inputs[1].Comments) != 3 {
		t.Fatalf("不限轮行内应含 P0/P1/P2 共 3 条: %+v", inputs)
	}
}

// seedMixedFindingsRun 一条 completed 的代码运行，带 P0/P1/P2/Note 各一条
// （行号 1–4 都落在调用方给的有效行内）。
func (e *reviewEngineEnv) seedMixedFindingsRun(t *testing.T, repo string, pr int, sha string, now time.Time) *model.ReviewRun {
	t.Helper()
	run := e.seedRun(t, repo, pr, sha, model.ReviewRunCompleted, now)
	result := `{"summary":"混合","findings":[
		{"level":"P0","file":"a","line":1,"body":"阻断"},
		{"level":"P1","file":"a","line":2,"body":"bug"},
		{"level":"P2","file":"a","line":3,"body":"边界"},
		{"level":"Note","file":"a","line":4,"body":"备注"}],
		"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.svc.db.Model(run).Updates(map[string]any{"result_json": result}).Error; err != nil {
		t.Fatalf("seed result: %v", err)
	}
	return run
}

// TestFetchInlineLinesOverCapNeed INT-204：超限轮 need 判定只认 P0/P1——
// 只有 P2 发现时不读 diff；不限轮照常读。
func TestFetchInlineLinesOverCapNeed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 22, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-need", RoundNumber: 4, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	run := e.seedRun(t, repo, 22, "sha-need", model.ReviewRunCompleted, now)
	result := `{"summary":"仅 P2","findings":[
		{"level":"P2","file":"a","line":3,"body":"边界"}],
		"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.svc.db.Model(run).Updates(map[string]any{"result_json": result}).Error; err != nil {
		t.Fatalf("seed result: %v", err)
	}
	client, err := e.review.reviewClient(context.Background(), model.ForgeIntegrationBuiltinGitHubID, repo, 1)
	if err != nil {
		t.Fatalf("reviewClient: %v", err)
	}

	// 超限：只有 P2 → 不读 diff、无有效行。
	lines, err := e.review.fetchInlineLines(context.Background(), client, ledger, []model.ReviewRun{*run}, true)
	if err != nil || lines != nil {
		t.Fatalf("超限仅 P2 不应读 diff: lines=%v err=%v", lines, err)
	}
	if n := e.fake.CallCounts()["GetPullRequestDiff"]; n != 0 {
		t.Fatalf("超限仅 P2 不应触发 diff 读取, got %d", n)
	}

	// 不超限：同一份结论会读 diff。
	if _, err := e.review.fetchInlineLines(context.Background(), client, ledger, []model.ReviewRun{*run}, false); err != nil {
		t.Fatalf("不限轮 fetchInlineLines: %v", err)
	}
	if n := e.fake.CallCounts()["GetPullRequestDiff"]; n != 1 {
		t.Fatalf("不限轮应读一次 diff, got %d", n)
	}
}

// TestReviewSettingsRoundCap INT-204 设置面：GET 无行默认 3、PUT 可保存回读、
// 越界（<1 或 >20）返回与组合校验同风格的 400。
func TestReviewSettingsRoundCap(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "cap-user")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "cap-env")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-cap")
	rs, _ := readyReviewService(t, svc)

	input := func(cap *int) *api.ReviewSettingsInput {
		in := validSettingsInput(env.ID)
		in.RoundCap = cap
		return in
	}

	// 无设置行时 GET 返回默认 3。
	got, err := rs.GetSettings(context.Background(), user.ID, project.ID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got.RoundCap != 3 {
		t.Fatalf("无行 RoundCap = %d, want 3", got.RoundCap)
	}

	// PUT round_cap=5 → GET 回读 5。
	saved, err := rs.UpdateSettings(context.Background(), user.ID, project.ID, input(api.Ptr(5)))
	if err != nil {
		t.Fatalf("put round_cap=5: %v", err)
	}
	if saved.RoundCap != 5 {
		t.Fatalf("回读 RoundCap = %d, want 5", saved.RoundCap)
	}
	got, err = rs.GetSettings(context.Background(), user.ID, project.ID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got.RoundCap != 5 {
		t.Fatalf("GET RoundCap = %d, want 5", got.RoundCap)
	}

	// 省略（nil）与显式 0 都按默认 3 落库。
	saved, err = rs.UpdateSettings(context.Background(), user.ID, project.ID, input(nil))
	if err != nil {
		t.Fatalf("put round_cap=nil: %v", err)
	}
	if saved.RoundCap != 3 {
		t.Fatalf("省略 round_cap 应存默认 3, got %d", saved.RoundCap)
	}
	zero := 0
	saved, err = rs.UpdateSettings(context.Background(), user.ID, project.ID, input(&zero))
	if err != nil {
		t.Fatalf("put round_cap=0: %v", err)
	}
	if saved.RoundCap != 3 {
		t.Fatalf("round_cap=0 应存默认 3, got %d", saved.RoundCap)
	}

	// 越界 → 400（ErrInvalidParams 同款）。
	for _, bad := range []int{-1, 21} {
		b := bad
		_, err := rs.UpdateSettings(context.Background(), user.ID, project.ID, input(&b))
		if code := appErrorCode(t, err); code != errs.ErrInvalidParams.Code {
			t.Fatalf("round_cap=%d: code = %d, want %d (err=%v)", bad, code, errs.ErrInvalidParams.Code, err)
		}
	}
}

// TestCodeRunsVisualInputsP2NotBlocking INT-204 审查 R1：视觉衔接的「全净」
// 口径与 commit status 一致——只有 P2 不再阻断（否则 P2-only 的 UI PR 绿灯
// 合入却从未做过视觉审查）；P0/P1 与解析失败照常阻断。
func TestCodeRunsVisualInputsP2NotBlocking(t *testing.T) {
	p2Only := model.ReviewRun{Kind: model.ReviewKindCode, Status: model.ReviewRunCompleted,
		ResultJSON: `{"summary":"s","findings":[{"level":"P2","file":"a.go","line":1,"body":"边界"}],
			"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`}
	allTerminal, allOK, anyUI, anySince := codeRunsVisualInputs([]model.ReviewRun{p2Only})
	if !allTerminal || !allOK || !anyUI || anySince {
		t.Fatalf("P2-only: terminal=%v ok=%v anyUI=%v anySince=%v, want true/true/true/false",
			allTerminal, allOK, anyUI, anySince)
	}

	p1 := p2Only
	p1.ResultJSON = `{"summary":"s","findings":[{"level":"P1","file":"a.go","line":1,"body":"bug"}],
		"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`
	if _, allOK, _, _ := codeRunsVisualInputs([]model.ReviewRun{p1}); allOK {
		t.Fatal("P1 应阻断视觉衔接")
	}

	running := p2Only
	running.Status = model.ReviewRunRunning
	if allTerminal, allOK, _, _ := codeRunsVisualInputs([]model.ReviewRun{running}); allTerminal || allOK {
		t.Fatal("在途运行应 terminal=false ok=false")
	}

	broken := p2Only
	broken.ResultJSON = `{broken`
	if _, allOK, _, _ := codeRunsVisualInputs([]model.ReviewRun{broken}); allOK {
		t.Fatal("结论不可解析应阻断视觉衔接")
	}

	// prior(open) 的 P1 是遗留阻断（审查 R6）：视觉衔接的全净口径与
	// summarizeCodeRuns 同判，否则代码状态红、视觉却在待修代码上启动。
	p1Prior := model.ReviewRun{Kind: model.ReviewKindCode, Status: model.ReviewRunCompleted,
		ResultJSON: `{"summary":"s","findings":[],
			"prior":[{"level":"P1","file":"a.go","body":"遗留","status":"open"}],
			"ui_changed":true,"ui_changed_since_last_visual":false}`}
	if _, allOK, _, _ := codeRunsVisualInputs([]model.ReviewRun{p1Prior}); allOK {
		t.Fatal("prior open P1 应阻断全净判定")
	}
}

// TestCodeHistoryCarriesOpenPrior INT-204 审查 R1：增量提示词要求核对仍为
// open 的条目，历史上下文必须把上一轮 prior(open) 带出来，否则模型标成
// open 的问题会在下一轮消失、复核断链（对齐 visualHistory 的补偿）。
func TestCodeHistoryCarriesOpenPrior(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	prev := e.seedRun(t, repo, 40, "sha-old", model.ReviewRunCompleted, now)
	prevResult := `{"summary":"上一轮","findings":[{"level":"P1","file":"a.go","line":1,"body":"旧发现"}],
		"prior":[{"level":"P1","file":"a.go","line":1,"body":"作者驳回了但我认为仍成立","status":"open"}],
		"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.svc.db.Model(prev).Updates(map[string]any{"result_json": prevResult}).Error; err != nil {
		t.Fatalf("seed prev result: %v", err)
	}
	finished := now.Add(-time.Hour)
	e.svc.db.Model(prev).Updates(map[string]any{"finished_at": finished})

	cur := e.seedRun(t, repo, 40, "sha-new", model.ReviewRunDispatched, now)
	e.svc.db.Model(cur).Updates(map[string]any{"round_number": 2, "base_ref": "main"})

	// 第二个组合各自标同一未解决条目（审查 R5 跨组合去重）：历史只渲染一次，
	// 否则下一轮会被诱导输出重复 prior。
	prev2 := e.seedRun(t, repo, 40, "sha-old", model.ReviewRunCompleted, now)
	e.svc.db.Model(prev2).Updates(map[string]any{
		"base_ref": "main", "instance_id": "o4-mini", "result_json": prevResult,
		"finished_at": finished,
	})

	h, err := e.review.codeHistory(context.Background(), cur)
	if err != nil {
		t.Fatalf("codeHistory: %v", err)
	}
	for _, want := range []string{"仍未解决 · P1", "a.go:1", "作者驳回了但我认为仍成立"} {
		if !strings.Contains(h, want) {
			t.Fatalf("历史上下文缺上一轮 prior(open) 内容 %q:\n%s", want, h)
		}
	}
	if n := strings.Count(h, "作者驳回了但我认为仍成立"); n != 1 {
		t.Fatalf("跨组合重复的 open prior 应只渲染一次, got %d:\n%s", n, h)
	}
}

// TestRunSummaryCodeCarriesOpenPrior 审查 R6：详情抽屉的代码投影带上
// prior(open) 遗留项——阻塞项（含级别缺失）计入 findings_count 并以
// 「上轮遗留：」前缀陈列，P2 遗留只陈列不计数；与 CodeStatus=failure 的
// 红绿一致，抽屉不再是「0 条发现」。
func TestRunSummaryCodeCarriesOpenPrior(t *testing.T) {
	run := model.ReviewRun{Kind: model.ReviewKindCode, Status: model.ReviewRunCompleted,
		ResultJSON: `{"summary":"s","findings":[{"level":"P2","file":"a.go","body":"边界"}],
			"prior":[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"},
			         {"body":"没写级别","status":"open"},
			         {"level":"P2","file":"y.go","body":"遗留P2","status":"open"}],
			"ui_changed":false}`}
	sum := runSummary(&run, nil)
	if sum.FindingsCount == nil || *sum.FindingsCount != 3 {
		t.Fatalf("findings_count = %v, want 3（P2 finding + 两条阻塞遗留，P2 遗留不计数）", sum.FindingsCount)
	}
	for _, want := range []string{"上轮遗留：遗留P1", "上轮遗留：没写级别", "上轮遗留：遗留P2"} {
		found := false
		for _, f := range sum.Findings {
			if strings.Contains(f.Body, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("详情投影缺遗留条目 %q: %+v", want, sum.Findings)
		}
	}
}

// TestReviewCountResultCodeOpenPrior 审查 R6：Web 活动计数的代码分支把
// prior(open) 阻塞项落进 P0/P1 桶（级别缺失按 P1 兜底）、fixed/P2 不计——
// needs_action 的 P0/P1 口径与 GitHub status 红绿一致。
func TestReviewCountResultCodeOpenPrior(t *testing.T) {
	raw := `{"summary":"s","findings":[],
		"prior":[{"level":"P1","file":"a","body":"x","status":"open"},
		         {"body":"nil 级别","status":"open"},
		         {"level":"P0","file":"b","body":"z","status":"open"},
		         {"level":"P2","file":"c","body":"y","status":"open"},
		         {"level":"P1","file":"d","body":"fixed","status":"fixed"}],
		"ui_changed":false}`
	findings := api.ReviewFindingCounts{}
	if ok := reviewCountResult(model.ReviewKindCode, raw, &findings, map[string]bool{}); !ok {
		t.Fatal("reviewCountResult 应解析成功")
	}
	if findings.P0 != 1 || findings.P1 != 2 || findings.P2 != 0 {
		t.Fatalf("计数桶 = %+v, want P0=1 P1=2 P2=0", findings)
	}
}

// TestSummarizeCodeRunsPriorDedupedAcrossRuns INT-207：多组合增量轮里同一
// 历史遗留被每个组合各自标 open，commit status 描述按（级别+定位+正文）跨
// 运行去重——1 条实际遗留显示 1 条，不再按组合放大；不同定位/正文的仍各算
// 一条。红绿判定不受影响（遗留本身仍判红）。
func TestSummarizeCodeRunsPriorDedupedAcrossRuns(t *testing.T) {
	// 两个组合各自报同一条遗留（同键），第二个组合另报一条不同定位+正文的：
	// 去重后 2 条（修复前按组合求和是 3 条）。
	st, desc := summarizeCodeRuns([]model.ReviewRun{
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}]`),
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"},
		                     {"level":"P0","file":"y.go","line":2,"body":"另一条","status":"open"}]`),
	}, false)
	if st != "failure" || !strings.Contains(desc, "遗留未解决 2 条") {
		t.Fatalf("跨组合去重: state=%q desc=%q, want 遗留未解决 2 条", st, desc)
	}

	// 同文件同行但正文不同是两条遗留，不合并。
	st, desc = summarizeCodeRuns([]model.ReviewRun{
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"第一种描述","status":"open"}]`),
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"第二种描述","status":"open"}]`),
	}, false)
	if st != "failure" || !strings.Contains(desc, "遗留未解决 2 条") {
		t.Fatalf("同定位不同正文应各算一条: state=%q desc=%q", st, desc)
	}

	// 无行号的遗留按文件定位参与去重：两组合同文件同正文只算一条。
	st, desc = summarizeCodeRuns([]model.ReviewRun{
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","body":"无行遗留","status":"open"}]`),
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","body":"无行遗留","status":"open"}]`),
	}, false)
	if st != "failure" || !strings.Contains(desc, "遗留未解决 1 条") {
		t.Fatalf("无行号遗留也应去重: state=%q desc=%q", st, desc)
	}

	// 一个组合标 fixed 后不再是遗留：另一组合的 open 仍计一条。
	st, desc = summarizeCodeRuns([]model.ReviewRun{
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"fixed"}]`),
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}]`),
	}, false)
	if st != "failure" || !strings.Contains(desc, "遗留未解决 1 条") {
		t.Fatalf("fixed 不计、另一组合的 open 计一条: state=%q desc=%q", st, desc)
	}
}

// TestBuildCodeCommentPriorDedupedAcrossRuns INT-207：顶层评论一句话结论的
// 「遗留未解决 N 条」跨组合去重；各组合分节仍完整渲染自己的遗留（信息不丢，
// 与 R7 处置的「各组合分节都完整渲染」一致）。
func TestBuildCodeCommentPriorDedupedAcrossRuns(t *testing.T) {
	dup := []model.ReviewRun{
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}]`),
		int204Run(t, `[]`, `[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}]`),
	}
	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890",
		links: githubLinks("https://github.com"), RoundNumber: 2, RoundCap: 3, ReviewedHeads: 2}
	body := buildCodeComment(meta, dup, nil, false, false)
	if !strings.Contains(body, "遗留未解决 1 条。") {
		t.Fatalf("结论的遗留计数应去重为 1 条:\n%s", body)
	}
	// 信息不丢：同一条遗留在每个组合分节各渲染一次（两个组合都报了它）。
	if n := strings.Count(body, "- 仍未解决 · P1 x.go:9：遗留P1"); n != 2 {
		t.Fatalf("各组合分节应完整渲染遗留（出现 2 次），实际 %d 次:\n%s", n, body)
	}

	// 超限轮同口径：去重后的遗留阻断计数进结论。
	meta.RoundNumber = 4
	meta.ReviewedHeads = 4
	body = buildCodeComment(meta, dup, nil, false, false)
	if !strings.Contains(body, "遗留未解决 1 条。") {
		t.Fatalf("超限轮结论的遗留计数也应去重:\n%s", body)
	}
}

// TestReviewCountResultPriorDedupedAcrossRuns INT-207：Web 活动计数的代码与
// 视觉两分支都按（级别+定位+正文）在段内跨运行去重——同一历史遗留被多个
// 组合各自标 open 时首页/抽屉的计数不放大；跨段不去重。
func TestReviewCountResultPriorDedupedAcrossRuns(t *testing.T) {
	codeRun := func(prior string) model.ReviewRun {
		return int204Run(t, `[]`, prior)
	}
	issues := []api.ReviewActivityIssue{}
	findings := api.ReviewFindingCounts{}
	reviewCollectRunIssues([]model.ReviewRun{
		codeRun(`[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}]`),
		codeRun(`[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"},
		         {"level":"P0","file":"y.go","line":1,"body":"另一条","status":"open"}]`),
	}, api.ReviewActivityIssueKindCode, &findings, &issues)
	if findings.P0 != 1 || findings.P1 != 1 {
		t.Fatalf("代码段计数桶 = %+v, want P0=1 P1=1（同一条遗留只计一次）", findings)
	}

	// 视觉段同理：两个视觉组合各自标同一条 open worth_fixing 只计一次。
	visRun := func(body string) model.ReviewRun {
		raw, err := json.Marshal(map[string]any{
			"summary": "s", "findings": []any{}, "unmeasurable": []any{},
			"prior": []map[string]any{{"level": "worth_fixing", "body": body, "status": "open"}},
		})
		if err != nil {
			t.Fatalf("marshal visual result: %v", err)
		}
		return model.ReviewRun{Kind: model.ReviewKindVisual, Status: model.ReviewRunCompleted,
			ResultJSON: string(raw), Model: "m", InstanceID: "claude", EnvironmentID: "env"}
	}
	findings = api.ReviewFindingCounts{}
	reviewCollectRunIssues([]model.ReviewRun{visRun("对比度不足"), visRun("对比度不足")},
		api.ReviewActivityIssueKindVisual, &findings, &issues)
	if findings.WorthFixing != 1 {
		t.Fatalf("视觉段计数桶 = %+v, want worth_fixing=1（同一条遗留只计一次）", findings)
	}

	// 代码与视觉两段不去重：同一正文在两段各算一次（键空间不同）。
	findings = api.ReviewFindingCounts{}
	seen := map[string]bool{}
	if ok := reviewCountResult(model.ReviewKindCode,
		`{"summary":"s","findings":[],"prior":[{"level":"P1","file":"x.go","line":9,"body":"遗留P1","status":"open"}],"ui_changed":false}`,
		&findings, seen); !ok {
		t.Fatal("代码分支应解析成功")
	}
	if ok := reviewCountResult(model.ReviewKindVisual,
		`{"summary":"s","findings":[],"prior":[{"level":"worth_fixing","body":"遗留P1","status":"open"}],"unmeasurable":[]}`,
		&findings, seen); !ok {
		t.Fatal("视觉分支应解析成功")
	}
	if findings.P1 != 1 || findings.WorthFixing != 1 {
		t.Fatalf("跨段计数桶 = %+v, want P1=1 worth_fixing=1", findings)
	}
}

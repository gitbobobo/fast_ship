package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

// INT-150 回归（musiver PR #563 视觉状态红绿反复）：未解决的上一轮问题只写进
// prior(open) 时也要计入结论、评论、历史与 Web 计数；代码审查未终态时视觉
// 决策悬置；截图只换 URL 内容不变不重开视觉轮；视觉评论带短 SHA 与轮次。

// visComboRun 一个最小视觉运行（组合名 m · claude）。
func visComboRun(status model.ReviewRunStatus, result string) model.ReviewRun {
	return model.ReviewRun{
		Kind: model.ReviewKindVisual, Status: status, ResultJSON: result,
		Model: "m", InstanceID: "claude", EnvironmentID: "env",
	}
}

// int150Result PR #563 第 3→4 轮的形态：本轮 findings 只有 1 条 minor，上一
// 轮那条 worth_fixing 没被重写进 findings，只标了 prior.status=open。
const int150Result = `{"summary":"问题仍挂着","findings":[{"severity":"minor","screenshot":"界面","dimension":"spacing","body":"圆角略不一致","fix":"统一圆角"}],` +
	`"prior":[{"level":"worth_fixing","body":"对比度不足看不清次要按钮","status":"open"}],"unmeasurable":[]}`

// E.1 未解决的 prior(open) 计入失败判定与各处计数：minor / 非 open 状态不
// 计，open 且按 priorOpenBlocks 阻塞的（blocker/worth_fixing、nil 与未知级
// 别兜底）都算问题。
func TestVisualPriorOpenCountsAsFailure(t *testing.T) {
	var result api.ReviewVisualResult
	if err := json.Unmarshal([]byte(int150Result), &result); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	if n := visualFailureCount(&result); n != 1 {
		t.Fatalf("visualFailureCount = %d, want 1（prior open worth_fixing 计入）", n)
	}
	// 口径边界（修 B）：open 的级别按 priorOpenBlocks 判阻塞——minor 不计，
	// fixed/rejected 已了结不计；「没写级别」的 open 按阻塞兜底计入（显式标
	// 了仍未解决就不该让结论变绿）。
	minorPrior := &api.ReviewVisualResult{
		Findings: []api.ReviewVisualFinding{{Severity: api.Minor, Screenshot: "s", Dimension: api.Spacing, Body: "b", Fix: "f"}},
		Prior: []api.ReviewPriorDecision{
			{Body: "没写级别", Status: api.ReviewPriorDecisionStatusOpen},
			{Level: api.Ptr("minor"), Body: "降级了", Status: api.ReviewPriorDecisionStatusOpen},
			{Level: api.Ptr("blocker"), Body: "已修", Status: api.ReviewPriorDecisionStatusFixed},
			{Level: api.Ptr("worth_fixing"), Body: "驳回", Status: api.ReviewPriorDecisionStatusRejected, Reason: api.Ptr("有意为之")},
		},
	}
	if n := visualFailureCount(minorPrior); n != 1 {
		t.Fatalf("visualFailureCount = %d, want 1（nil 级别的 open 兜底计入，minor/fixed/rejected 不计）", n)
	}
	// 未知级别（代码审查的 P1）的 open 同样按阻塞兜底计入。
	p1Prior := &api.ReviewVisualResult{
		Prior: []api.ReviewPriorDecision{
			{Level: api.Ptr("P1"), Body: "误写了代码分级", Status: api.ReviewPriorDecisionStatusOpen},
		},
	}
	if n := visualFailureCount(p1Prior); n != 1 {
		t.Fatalf("visualFailureCount = %d, want 1（未知级别 P1 的 open 计入）", n)
	}

	// GitHub 状态：failure，描述与「发现 1 条问题」同量级。
	state, desc := summarizeVisualRuns([]model.ReviewRun{visComboRun(model.ReviewRunCompleted, int150Result)})
	if state != "failure" || !strings.Contains(desc, "发现 1 条问题") {
		t.Fatalf("summarizeVisualRuns = %q/%q, want failure/发现 1 条问题", state, desc)
	}
	if !visualResultHasFailure(int150Result) {
		t.Fatal("visualResultHasFailure = false, want true（prior open 兜底）")
	}

	// Web 侧「需要处理」计数同口径：blocker/worth_fixing 各加一。
	findings := &api.ReviewFindingCounts{}
	if !reviewCountResult(model.ReviewKindVisual, int150Result, findings) {
		t.Fatal("reviewCountResult = false, want true")
	}
	if findings.Blocker != 0 || findings.WorthFixing != 1 {
		t.Fatalf("counts = %+v, want worth_fixing 1（prior open 计入）", findings)
	}
}

// 修 A：审查记录投影（PR 详情抽屉）把 prior(open) 一并展示并按阻塞口径计数
// ——展示宽（全部 open 条目，含 minor/未分级），计数窄（只算阻塞级别），与
// GitHub status / 首页「需要处理」同一把尺，应用内不再出现「抽屉 0 条发现但
// 状态失败」的矛盾。遗留条目带「上轮遗留：」前缀，复核者能区分本轮新发现。
func TestRunSummaryVisualPriorOpenProjected(t *testing.T) {
	project := func(run model.ReviewRun) api.ReviewRunSummary {
		t.Helper()
		return runSummary(&run, nil)
	}

	// 本轮只有 minor（不计数），上轮 worth_fixing 只标 prior(open)（计数）：
	// findings_count = 1，列表两条——本轮原文 + 带前缀的遗留条目。
	run := visComboRun(model.ReviewRunCompleted, int150Result)
	sum := project(run)
	if sum.FindingsCount == nil || *sum.FindingsCount != 1 {
		t.Fatalf("FindingsCount = %v, want 1（minor 不计、prior open worth_fixing 计）", sum.FindingsCount)
	}
	if len(sum.Findings) != 2 {
		t.Fatalf("Findings = %d 条, want 2（本轮 minor + 上轮遗留）", len(sum.Findings))
	}
	if sum.Findings[0].Level != string(api.Minor) || sum.Findings[0].Body != "圆角略不一致" {
		t.Fatalf("Findings[0] = %+v, want 本轮 minor 原文", sum.Findings[0])
	}
	legacy := sum.Findings[1]
	if legacy.Level != string(api.WorthFixing) || legacy.Body != "上轮遗留：对比度不足看不清次要按钮" {
		t.Fatalf("Findings[1] = %+v, want 上轮遗留前缀的 worth_fixing", legacy)
	}
	if legacy.Screenshot != nil || legacy.Dimension != nil || legacy.Fix != nil {
		t.Fatalf("遗留条目 = %+v, want 截图/维度/改法留空", legacy)
	}

	// prior(open) 没写 level：计数按阻塞兜底（1 条），展示级别兜底为
	// worth_fixing——Level 字段非可空，留空会渲染残缺行。
	nilLevel, err := json.Marshal(&api.ReviewVisualResult{
		Prior: []api.ReviewPriorDecision{{Body: "上轮的阻断问题", Status: api.ReviewPriorDecisionStatusOpen}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sum = project(visComboRun(model.ReviewRunCompleted, string(nilLevel)))
	if sum.FindingsCount == nil || *sum.FindingsCount != 1 {
		t.Fatalf("FindingsCount = %v, want 1（nil 级别的 open 按阻塞兜底）", sum.FindingsCount)
	}
	if len(sum.Findings) != 1 || sum.Findings[0].Level != string(api.WorthFixing) ||
		sum.Findings[0].Body != "上轮遗留：上轮的阻断问题" {
		t.Fatalf("Findings = %+v, want 1 条上轮遗留且级别兜底 worth_fixing", sum.Findings)
	}

	// prior(open, minor)：不计数（FindingsCount = 0）但条目仍出现在列表里
	// ——展示宽口径让复核者看到全部遗留，计数只跟红绿判定走。
	minorOpen, err := json.Marshal(&api.ReviewVisualResult{
		Prior: []api.ReviewPriorDecision{{Level: api.Ptr("minor"), Body: "降级的遗留", Status: api.ReviewPriorDecisionStatusOpen}},
	})
	if err != nil {
		t.Fatalf("marshal minor: %v", err)
	}
	sum = project(visComboRun(model.ReviewRunCompleted, string(minorOpen)))
	if sum.FindingsCount == nil || *sum.FindingsCount != 0 {
		t.Fatalf("FindingsCount = %v, want 0（minor 的遗留不计数）", sum.FindingsCount)
	}
	if len(sum.Findings) != 1 || sum.Findings[0].Level != string(api.Minor) ||
		sum.Findings[0].Body != "上轮遗留：降级的遗留" {
		t.Fatalf("Findings = %+v, want minor 遗留仍展示", sum.Findings)
	}
}

// 修 B：视觉结论校验拒绝 open 缺 level / 非法 level，worth_fixing 与 minor
// 通过；代码结论（validateCodeResult）不做此校验，open 缺 level 仍放行——
// 口径不变，防止误伤历史形态的代码结论。
func TestValidateVisualResultPriorOpenRequiresLevel(t *testing.T) {
	visual := func(priorJSON string) *api.ReviewVisualResult {
		t.Helper()
		var r api.ReviewVisualResult
		body := `{"summary":"s","findings":[],"unmeasurable":[],"prior":[` + priorJSON + `]}`
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("seed visual result: %v", err)
		}
		return &r
	}
	for _, good := range []string{
		`{"level":"worth_fixing","body":"x","status":"open"}`,
		`{"level":"blocker","body":"x","status":"open"}`,
		`{"level":"minor","body":"x","status":"open"}`,
	} {
		if err := validateVisualResult(visual(good)); err != nil {
			t.Fatalf("validateVisualResult(%s) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{
		`{"body":"x","status":"open"}`,              // open 缺 level
		`{"level":"P1","body":"x","status":"open"}`, // 误写代码审查分级
	} {
		if err := validateVisualResult(visual(bad)); appErrorCode(t, err) != 40004 {
			t.Fatalf("validateVisualResult(%s) = %v, want 40004（open 缺/错 level 拒收）", bad, err)
		}
	}

	// fixed / rejected 不要求 level（已了结的条目不参与红绿计数）。
	if err := validateVisualResult(visual(`{"body":"x","status":"fixed"}`)); err != nil {
		t.Fatalf("fixed 缺 level 应通过: %v", err)
	}

	var code api.ReviewCodeResult
	codeBody := `{"summary":"s","findings":[],"prior":[{"body":"x","status":"open"}],"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := json.Unmarshal([]byte(codeBody), &code); err != nil {
		t.Fatalf("seed code result: %v", err)
	}
	if err := validateCodeResult(&code); err != nil {
		t.Fatalf("validateCodeResult = %v, want nil（代码校验口径不变）", err)
	}
}

// E.1 评论与历史：只写进 prior(open) 的未解决项在评论「仍未解决」段、下一轮
// 视觉历史的「仍未解决（更早一轮）」小节里都能看到，不会凭空消失。
func TestVisualPriorOpenStaysVisibleInCommentAndHistory(t *testing.T) {
	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890", links: githubLinks("https://github.com"), RoundNumber: 4}
	body := buildVisualComment(meta, []model.ReviewRun{visComboRun(model.ReviewRunCompleted, int150Result)}, nil, false)
	// 遵守契约的返回（findings 只写本轮新问题，未解决项只标 prior(open)）分区
	// 各归其位：minor 在「本轮新发现」、worth_fixing 在「仍未解决」。
	newIdx, unresolvedIdx := strings.Index(body, "**本轮新发现**"), strings.Index(body, "**仍未解决**")
	if newIdx < 0 || unresolvedIdx < 0 ||
		newIdx > strings.Index(body, "圆角略不一致") ||
		!strings.Contains(body, "- 仍未解决 · worth_fixing：对比度不足看不清次要按钮") {
		t.Fatalf("comment sections wrong:\n%s", body)
	}
	if newIdx > unresolvedIdx {
		t.Fatalf("新发现段应在仍未解决段之前:\n%s", body)
	}

	// 下一轮视觉历史（E.1 的 visualHistory 部分）。
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	prev := e.seedRun(t, repo, 82, "sha-h1", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prev).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "result_json": int150Result,
	})
	cur := e.seedRun(t, repo, 82, "sha-h2", model.ReviewRunDispatched, now)
	e.svc.db.Model(cur).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})
	cur.Kind = model.ReviewKindVisual
	cur.RoundNumber = 2

	history, err := e.review.visualHistory(context.Background(), cur)
	if err != nil {
		t.Fatalf("visualHistory: %v", err)
	}
	if !strings.Contains(history, "### 仍未解决（更早一轮）") ||
		!strings.Contains(history, "- worth_fixing：对比度不足看不清次要按钮") {
		t.Fatalf("history missing carried-over unresolved entry:\n%s", history)
	}
}

// E.2 代码审查未终态（有 running 运行）时同步：不写视觉状态、不建视觉运行、
// 不动截图指纹，决策悬置为 visualWait；代码全部终态后下一次同步照常开轮
// （等待不会卡死衔接）。
func TestVisualSyncWaitsForCodeTerminal(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 83, "sha-w", now)
	// 第二个代码组合还在跑：全部代码运行未终态。
	e.seedRun(t, repo, 83, "sha-w", model.ReviewRunRunning, now)

	e.review.syncPendingPRs(now)

	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 0 {
		t.Fatalf("statuses = %+v, want 0（代码未终态不写视觉状态）", got)
	}
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).Where("kind = ?", model.ReviewKindVisual).Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（代码未终态不开视觉轮）", visual)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 83", repo)
	if ledger.VisualStatus != "" || ledger.VisualScreensFingerprint != "" {
		t.Fatalf("ledger = %q/%q, want 双空（等待分支不动账本）",
			ledger.VisualStatus, ledger.VisualScreensFingerprint)
	}
	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（等待分支不探测）", e.media.anonCalls)
	}
	// 代码顶层评论照常发布（视觉提示不出现——视觉还没轮到）。
	codePosted := false
	for _, c := range e.fake.Comments(repo, 83) {
		if strings.Contains(c, reviewCodeCommentMarker) {
			codePosted = true
			if strings.Contains(c, "视觉审查尚未结束") {
				t.Fatalf("code comment = %q, 等待分支不应带视觉待办提示", c)
			}
		}
	}
	if !codePosted {
		t.Fatalf("comments = %v, want 代码顶层评论照常发布", e.fake.Comments(repo, 83))
	}

	// 第二个组合完成：下一次同步事件正常开视觉轮（判定不再悬置）。
	second := model.ReviewRun{Status: model.ReviewRunCompleted}
	if err := e.svc.db.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ? AND status = ?",
			repo, 83, "sha-w", model.ReviewRunRunning).
		Updates(map[string]any{
			"status":      second.Status,
			"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
		}).Error; err != nil {
		t.Fatalf("complete second run: %v", err)
	}
	e.setLedgerPending(t, repo, 83)
	e.review.syncPendingPRs(now.Add(time.Minute))
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ?", repo, 83, model.ReviewKindVisual).Find(&visualRuns)
	if len(visualRuns) == 0 {
		t.Fatal("代码全部终态后应开视觉轮")
	}
	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 1 || got[0].State != "pending" {
		t.Fatalf("statuses = %+v, want 一条 pending", got)
	}
}

// E.3 只换 URL、内容不变：URL 集合指纹不同但下载内容相同 → 不算截图变化；
// 上一轮通过 → 沿用 success（不新建视觉轮），上一轮失败 → 沿用 failure；
// 新指纹（含内容维度）落账，后续同步事件不再重探。
func TestVisualSyncSameContentDifferentURLsCarries(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	bodyA := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	bodyB := "![重传](https://uiv.frp.musiver.cn/f/x-reupload.png)"
	urlFPA := screenshotFingerprint(collectScreenshotURLs(bodyA))
	urlFPB := screenshotFingerprint(collectScreenshotURLs(bodyB))
	if urlFPA == urlFPB {
		t.Fatal("前置不成立：两个 URL 集合的指纹应不同")
	}
	contentFP := screenshotContentFingerprint([]string{e.media.cached.Path})
	if contentFP == "" {
		t.Fatal("前置不成立：桩缓存文件应可读出内容指纹")
	}

	// 场景 A：上一轮视觉通过，本轮作者重传截图（URL 全换、内容不变）。
	e.fake.SetPR(repo, 84, &githubapi.PullRequest{
		RepoFullName: repo, Number: 84, HeadSHA: "sha-sc", Body: bodyB,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-sc")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	passed := e.seedRun(t, repo, 84, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(passed).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 84, "sha-sc", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	// 账本记的是旧 URL 集合 + 内容指纹（上一轮探测/开轮时落账的新格式）。
	storedA := encodeScreensFingerprint(screensFingerprint{urlFP: urlFPA, contentFP: contentFP})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 84, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-sc", RoundNumber: 2,
		VisualScreensFingerprint: storedA, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// 内容没变 → 不重开视觉轮，沿用 success。
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ?", model.ReviewKindVisual, "sha-sc").Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（内容相同不重开视觉轮）", visual)
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "success" {
		t.Fatalf("statuses = %+v, want 一条 success（沿用上一轮通过）", statuses)
	}
	if !strings.Contains(statuses[0].Description, "沿用上一轮，截图未变化") {
		t.Fatalf("desc = %q, want 截图未变化的沿用文案", statuses[0].Description)
	}
	if e.media.anonCalls != 1 {
		t.Fatalf("anonCalls = %d, want 1（URL 集合变了要探测一次算内容指纹）", e.media.anonCalls)
	}
	// 新指纹落账：内容相同的判定存住，后续同步事件不再重探。
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 84", repo)
	wantFP := encodeScreensFingerprint(screensFingerprint{urlFP: urlFPB, contentFP: contentFP})
	if ledger.VisualScreensFingerprint != wantFP {
		t.Fatalf("fingerprint = %q, want %q（URL 换了、内容没变的新指纹入账）", ledger.VisualScreensFingerprint, wantFP)
	}
	e.setLedgerPending(t, repo, 84)
	e.review.syncPendingPRs(now.Add(time.Minute))
	if e.media.anonCalls != 1 {
		t.Fatalf("anonCalls = %d, want 仍为 1（同一集合+内容不再重探）", e.media.anonCalls)
	}

	// 场景 B：上一轮视觉失败（worth_fixing 挂着），同样只换 URL 内容不变
	// → 沿用 failure，且新指纹一并落账（避免后续事件反复探测）。
	e.fake.SetPR(repo, 85, &githubapi.PullRequest{
		RepoFullName: repo, Number: 85, HeadSHA: "sha-sf", Body: bodyB,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	failed := e.seedRun(t, repo, 85, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(failed).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有问题","findings":[{"severity":"worth_fixing","screenshot":"界面","dimension":"color","body":"对比度不足","fix":"加深文字"}],"prior":[],"unmeasurable":[]}`,
	})
	run2 := e.seedRun(t, repo, 85, "sha-sf", model.ReviewRunCompleted, now)
	e.svc.db.Model(run2).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 85, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-sf", RoundNumber: 2,
		VisualScreensFingerprint: storedA, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger B: %v", err)
	}
	e.review.syncPendingPRs(now.Add(2 * time.Minute))

	// fake 的状态序列跨 PR 共享，按本场景 head 过滤（不引 githubapp 内部类型）。
	statuses = e.fake.StatusesForContext(reviewVisualStatusContext)
	countB, stateB, descB := 0, "", ""
	for _, s := range statuses {
		if s.SHA == "sha-sf" {
			countB, stateB, descB = countB+1, s.State, s.Description
		}
	}
	if countB != 1 || stateB != "failure" {
		t.Fatalf("statuses = %+v, want 一条 failure（沿用上一轮失败）", statuses)
	}
	if !strings.Contains(descB, "截图复核意见尚未处理（上轮 1 条）") {
		t.Fatalf("desc = %q, want 沿用 failure 且条数按 prior(open) 口径", descB)
	}
	var ledgerB model.ReviewPullRequest
	e.svc.db.First(&ledgerB, "repo_full_name = ? AND pr_number = 85", repo)
	if ledgerB.VisualScreensFingerprint != wantFP {
		t.Fatalf("fingerprint = %q, want %q（沿用 failure 也要落新指纹）", ledgerB.VisualScreensFingerprint, wantFP)
	}
	// 带过评论保留上一轮意见，标题带本 head 短 SHA 与轮次。
	found := false
	for _, c := range e.fake.Comments(repo, 85) {
		if strings.Contains(c, reviewVisualCommentMarker) &&
			strings.Contains(c, "## 视觉审查 · sha-sf · 第 2 轮") &&
			strings.Contains(c, "对比度不足") {
			found = true
		}
	}
	if !found {
		t.Fatalf("comments = %v, want 带标题的沿用 failure 评论", e.fake.Comments(repo, 85))
	}
}

// E.4 视觉评论标题与分区：非沿用评论分「本轮新发现」「仍未解决」两段；沿用
// 评论开头指向上轮轮次与 head，被沿用的发现归入「仍未解决」；「截图无法下
// 载」评论同样带标题；两段都空时只输出标题+summary。
func TestBuildVisualCommentTitleAndSections(t *testing.T) {
	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890", links: githubLinks("https://github.com"), RoundNumber: 3}
	result := `{"summary":"两处问题","findings":[` +
		`{"severity":"minor","screenshot":"s2","dimension":"spacing","body":"次要点","fix":"微调"},` +
		`{"severity":"blocker","screenshot":"s1","dimension":"hierarchy","body":"按钮重叠","fix":"调层级"}],` +
		`"prior":[{"level":"worth_fixing","body":"对比度不足","status":"open"},` +
		`{"level":"minor","body":"已改好","status":"fixed"}],"unmeasurable":[{"screenshot":"s3","reason":"加载失败"}]}`
	body := buildVisualComment(meta, []model.ReviewRun{visComboRun(model.ReviewRunCompleted, result)}, nil, false)

	if !strings.Contains(body, reviewVisualCommentMarker) ||
		!strings.Contains(body, "## 视觉审查 · abcdef1 · 第 3 轮") {
		t.Fatalf("missing marker/title:\n%s", body)
	}
	// 严重度排序（blocker 在 minor 前）+ 两段分区。
	if i, j := strings.Index(body, "**本轮新发现**"), strings.Index(body, "按钮重叠"); !(i >= 0 && i < j) {
		t.Fatalf("findings section wrong:\n%s", body)
	}
	if strings.Index(body, "按钮重叠") > strings.Index(body, "次要点") {
		t.Fatalf("findings should sort blocker first:\n%s", body)
	}
	if !strings.Contains(body, "- 无法判断 · s3：加载失败") {
		t.Fatalf("missing unmeasurable line:\n%s", body)
	}
	if strings.Contains(body, "- 仍未解决 · minor：已改好") {
		t.Fatalf("fixed prior 不应出现在仍未解决段:\n%s", body)
	}
	if !strings.Contains(body, "**仍未解决**\n- 仍未解决 · worth_fixing：对比度不足") {
		t.Fatalf("missing unresolved section:\n%s", body)
	}
	// 分区顺序：本轮新发现在前，仍未解决在后。
	if i, j := strings.Index(body, "**本轮新发现**"), strings.Index(body, "**仍未解决**"); i > j {
		t.Fatalf("section order wrong:\n%s", body)
	}

	// 沿用：指向上轮轮次与 head；上轮发现归入「仍未解决」而不是「本轮新发现」。
	prior := visComboRun(model.ReviewRunCompleted, result)
	prior.RoundNumber = 2
	prior.HeadSHA = "1234567890abcdef"
	carried := buildVisualComment(meta, nil, []model.ReviewRun{prior}, true)
	if !strings.Contains(carried, "沿用上一轮视觉审查结论（第 2 轮 · head 1234567）") {
		t.Fatalf("missing carry intro with prior round/head:\n%s", carried)
	}
	if strings.Contains(carried, "**本轮新发现**") {
		t.Fatalf("carried findings 不得标成本轮新发现:\n%s", carried)
	}
	if !strings.Contains(carried, "**仍未解决**") || !strings.Contains(carried, "按钮重叠") {
		t.Fatalf("carried findings 应归入仍未解决:\n%s", carried)
	}

	// 截图全部无法下载的评论同样带标题（短 SHA · 轮次）。
	un := buildScreensUnavailableComment(meta, []model.ReviewRun{prior})
	if !strings.Contains(un, "## 视觉审查 · abcdef1 · 第 3 轮") ||
		!strings.Contains(un, "全部无法下载") || !strings.Contains(un, "按钮重叠") {
		t.Fatalf("screens-unavailable comment wrong:\n%s", un)
	}

	// 两段都空：只输出标题 + summary。
	empty := `{"summary":"没问题","findings":[],"prior":[],"unmeasurable":[]}`
	plain := buildVisualComment(meta, []model.ReviewRun{visComboRun(model.ReviewRunCompleted, empty)}, nil, false)
	if strings.Contains(plain, "**本轮新发现**") || strings.Contains(plain, "**仍未解决**") {
		t.Fatalf("empty combo 不应输出分区标题:\n%s", plain)
	}
	if !strings.Contains(plain, "m · claude\n没问题") {
		t.Fatalf("empty combo 应保留标题与 summary:\n%s", plain)
	}
}

// 截图指纹解析/编码：现行 u:/c: 形态、旧版 none: 前缀与最早的裸 urlFP 都能
// 解析；编码在内容未知时退回裸 urlFP（不虚构内容维度）。
func TestScreensFingerprintParseEncode(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   screensFingerprint
	}{
		{"空值", "", screensFingerprint{}},
		{"现行结清态", "u:abc123", screensFingerprint{urlFP: "abc123", settledNone: true}},
		{"旧版结清态", "none:abc123", screensFingerprint{urlFP: "abc123", settledNone: true}},
		{"URL+内容", "c:abc:def456", screensFingerprint{urlFP: "abc", contentFP: "def456"}},
		{"URL+空内容", "c:abc:", screensFingerprint{urlFP: "abc"}},
		{"最早裸指纹", "abc123", screensFingerprint{urlFP: "abc123"}},
		// urlFP 是 hex，可能以 c 开头但没有冒号——不能被 c: 前缀误咬。
		{"c 开头的裸指纹", "c0ffee0011223344", screensFingerprint{urlFP: "c0ffee0011223344"}},
	}
	for _, c := range cases {
		if got := parseScreensFingerprint(c.stored); got != c.want {
			t.Fatalf("%s: parse(%q) = %+v, want %+v", c.name, c.stored, got, c.want)
		}
	}
	for _, fp := range []screensFingerprint{
		{urlFP: "abc"},
		{urlFP: "abc", contentFP: "def"},
		{urlFP: "abc", settledNone: true},
	} {
		if got := parseScreensFingerprint(encodeScreensFingerprint(fp)); got != fp {
			t.Fatalf("round trip %+v → %q → %+v", fp, encodeScreensFingerprint(fp), got)
		}
	}
	// 结清态一律编码成现行 u: 前缀（none: 只进不出）。
	if got := encodeScreensFingerprint(screensFingerprint{urlFP: "abc", settledNone: true}); got != "u:abc" {
		t.Fatalf("encode settled = %q, want u:abc", got)
	}
}

// screenshotContentFingerprint 按路径取内容指纹：测试构造期望值用（生产探测
// 路径在下载当场流式算哈希，不按路径二次读盘）。
func screenshotContentFingerprint(paths []string) string {
	hashes := make([]string, 0, len(paths))
	for _, p := range paths {
		sum, err := hashFileSHA256(p)
		if err != nil {
			return ""
		}
		hashes = append(hashes, sum)
	}
	return screenshotContentFingerprintFromHashes(hashes)
}

// 内容指纹：同字节不同路径同指纹（重传场景），字节不同指纹不同；排序不依赖
// 引用顺序。
func TestScreenshotContentFingerprint(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	p1, p2, p3 := write("a.bin", "same-bytes"), write("b.bin", "same-bytes"), write("c.bin", "other-bytes")

	if screenshotContentFingerprint([]string{p1}) != screenshotContentFingerprint([]string{p2}) {
		t.Fatal("相同字节的不同文件应得到相同内容指纹")
	}
	if screenshotContentFingerprint([]string{p1}) == screenshotContentFingerprint([]string{p3}) {
		t.Fatal("不同字节应得到不同内容指纹")
	}
	if screenshotContentFingerprint([]string{p1, p3}) != screenshotContentFingerprint([]string{p3, p1}) {
		t.Fatal("内容指纹不应依赖引用顺序")
	}
	// 文件读不出来 → 内容未知（空串）。
	if got := screenshotContentFingerprint([]string{filepath.Join(dir, "missing.bin")}); got != "" {
		t.Fatalf("missing file fp = %q, want 空（内容未知）", got)
	}
}

// perURLMedia 按 URL 覆盖返回内容的下载桩：包一层 fakeReviewMedia，命中
// overrides 的 URL 返回各自的缓存文件（内容可不同），其余沿用原桩行为。
type perURLMedia struct {
	*fakeReviewMedia
	overrides map[string]*githubmedia.CachedMedia
}

func (f *perURLMedia) FetchAndCache(ctx context.Context, url string) (*githubmedia.CachedMedia, error) {
	if c, ok := f.overrides[url]; ok {
		f.anonCalls++
		return c, nil
	}
	return f.fakeReviewMedia.FetchAndCache(ctx, url)
}

// writeShot 落一个指定字节的临时「截图」文件并包成 CachedMedia。
func writeShot(t *testing.T, data string) *githubmedia.CachedMedia {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shot-"+data+".png")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write shot: %v", err)
	}
	return githubmedia.CachedImage(path, "image/png", int64(len(data)))
}

// E.2 变体：上一轮视觉通过 + 本轮代码还有一个 running 运行 + 截图集合未变。
// visualWait 必须排在沿用判定之前——代码结论还没落定时任何视觉判断（包括
// 沿用 success）都可能建立在即将被推翻的输入上，一条视觉 status 都不能写。
func TestVisualSyncWaitsForCodeTerminalBeforeCarrySuccess(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 97, &githubapi.PullRequest{
		RepoFullName: repo, Number: 97, HeadSHA: "sha-wp", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-wp")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉 completed 且无发现（通过）。
	prior := e.seedRun(t, repo, 97, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	// 本轮（round 2）两个代码组合：一个干净完成、一个还在跑。
	run := e.seedRun(t, repo, 97, "sha-wp", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	running := e.seedRun(t, repo, 97, "sha-wp", model.ReviewRunRunning, now)
	e.svc.db.Model(running).Update("round_number", 2)
	// 截图集合未变（账本指纹就是当前集合）：沿用 success 的其余前提齐了，
	// 只差代码未终态这一条。
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 97, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-wp", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// 一条视觉 status 都不能有（旧实现会沿用上一轮 success），也不建视觉
	// 运行、不动账本、不探测。
	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 0 {
		t.Fatalf("statuses = %+v, want 0（代码未终态不沿用上一轮 success）", got)
	}
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ?", model.ReviewKindVisual, "sha-wp").Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（代码未终态不开轮）", visual)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 97", repo)
	if ledger.VisualStatus != "" || ledger.VisualScreensFingerprint != fp {
		t.Fatalf("ledger = %q/%q, want 状态空、指纹原样（等待分支不动账本）",
			ledger.VisualStatus, ledger.VisualScreensFingerprint)
	}
	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（等待分支不探测）", e.media.anonCalls)
	}
	// 代码顶层评论照常发布；上一轮是通过的，不得带遗留提示。
	codePosted := false
	for _, c := range e.fake.Comments(repo, 97) {
		if strings.Contains(c, reviewCodeCommentMarker) {
			codePosted = true
			if strings.Contains(c, "截图复核意见尚未处理") {
				t.Fatalf("code comment = %q, 上一轮通过不应带遗留提示", c)
			}
		}
	}
	if !codePosted {
		t.Fatalf("comments = %v, want 代码顶层评论照常发布", e.fake.Comments(repo, 97))
	}
}

// 修 1：部分下载成功也开轮，账本指纹至少推进 URL 维度（内容未知不写内容）。
// 下一轮同一集合靠 URL 指纹判「未变化」——「正常图 + 恒失败引用」的组合
// 内容指纹永远未知，不推进 URL 维度就会每轮重探重开。
func TestVisualSyncPartialStartAdvancesURLFingerprint(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![好图](https://uiv.frp.musiver.cn/f/ok.png) ![坏图](https://uiv.frp.musiver.cn/f/dead.png)"
	e.fake.SetPR(repo, 98, &githubapi.PullRequest{
		RepoFullName: repo, Number: 98, HeadSHA: "sha-p1", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-pa")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	run := e.seedRun(t, repo, 98, "sha-p1", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,` +
			`"ui_changed_since_last_visual":true}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 98, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-p1", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	e.media.failURLs = map[string]error{
		"https://uiv.frp.musiver.cn/f/dead.png": errors.New("404 not found"),
	}
	e.review.syncPendingPRs(now)

	// visualStart 发生，账本指纹 = 本集合的裸 urlFP（无旧内容维度）。
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ?", repo, 98, model.ReviewKindVisual).Find(&visualRuns)
	if len(visualRuns) != 1 {
		t.Fatalf("visual runs = %d, want 1（部分成功照常开轮）", len(visualRuns))
	}
	wantURLFP := screenshotFingerprint(collectScreenshotURLs(body))
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 98", repo)
	stored := parseScreensFingerprint(ledger.VisualScreensFingerprint)
	if stored != (screensFingerprint{urlFP: wantURLFP}) {
		t.Fatalf("fingerprint = %+v (%q), want 裸 urlFP %s（内容维度未知不入账）",
			stored, ledger.VisualScreensFingerprint, wantURLFP)
	}

	// round 1 视觉通过；round 2 同一截图集合、代码干净且距上次视觉无 UI 改动
	// → URL 指纹命中判「未变化」→ 沿用 success，全程不再探测。
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ? AND round_number = ?", model.ReviewKindVisual, "sha-p1", 1).
		Updates(map[string]any{
			"status":      model.ReviewRunCompleted,
			"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
		})
	e.fake.SetPR(repo, 98, &githubapi.PullRequest{
		RepoFullName: repo, Number: 98, HeadSHA: "sha-p2", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	run2 := e.seedRun(t, repo, 98, "sha-p2", model.ReviewRunCompleted, now.Add(time.Minute))
	e.svc.db.Model(run2).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 98).
		Updates(map[string]any{"head_sha": "sha-p2", "round_number": 2})
	e.setLedgerPending(t, repo, 98)
	e.review.syncPendingPRs(now.Add(time.Minute))

	if e.media.anonCalls != 2 {
		t.Fatalf("anonCalls = %d, want 仍为 2（URL 指纹命中，同一集合不重探）", e.media.anonCalls)
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	var carry int
	for _, s := range statuses {
		if s.SHA == "sha-p2" {
			carry++
			if s.State != "success" || !strings.Contains(s.Description, "沿用上一轮，截图未变化") {
				t.Fatalf("status = %+v, want 沿用 success（URL 集合未变判未变化）", s)
			}
		}
	}
	if carry != 1 {
		t.Fatalf("sha-p2 的视觉 status = %d 条, want 1", carry)
	}
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 98", repo)
	if ledger.VisualScreensFingerprint != wantURLFP {
		t.Fatalf("fingerprint = %q, want 原样保留 %s（沿用不覆盖）",
			ledger.VisualScreensFingerprint, wantURLFP)
	}
}

// 修 1 回归（审查构造的错配场景）：A 轮失败且账本存 A 的指纹；B 轮换截图、
// 部分下载成功开轮 → 账本指纹必须推进到 B 的 URL 集合；C 轮把截图换回 A 的
// URL 时不得命中 A 的旧指纹误判「未变化」、把 B 轮（不同截图）的 success 沿
// 用给 A 的截图——探测要发生，结论要重开轮。
func TestVisualSyncFingerprintTracksLatestVisualMaterial(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	bodyA := "![甲](https://uiv.frp.musiver.cn/f/a.png)"
	bodyB := "![乙一](https://uiv.frp.musiver.cn/f/b1.png) ![乙二](https://uiv.frp.musiver.cn/f/b2.png)"
	urlFPA := screenshotFingerprint(collectScreenshotURLs(bodyA))
	urlFPB := screenshotFingerprint(collectScreenshotURLs(bodyB))
	if urlFPA == urlFPB {
		t.Fatal("前置不成立：A/B 两个 URL 集合的指纹应不同")
	}
	contentA := screenshotContentFingerprint([]string{e.media.cached.Path})

	e.fake.SetPR(repo, 99, &githubapi.PullRequest{
		RepoFullName: repo, Number: 99, HeadSHA: "sha-b", Body: bodyB,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-ab")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// A 轮（round 1）视觉失败，账本存 A 的完整指纹（URL + 内容）。
	priorA := e.seedRun(t, repo, 99, "sha-a", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(priorA).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断问题","findings":[{"severity":"blocker","screenshot":"甲",` +
			`"dimension":"hierarchy","body":"按钮叠在文字上","fix":"调整层级"}],"prior":[],"unmeasurable":[]}`,
	})
	// B 轮（round 2）代码干净、UI 有改动且距上次视觉有 UI 改动。
	runB := e.seedRun(t, repo, 99, "sha-b", model.ReviewRunCompleted, now)
	e.svc.db.Model(runB).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
	})
	// B 的第二张图恒失败 → 部分下载成功开轮（内容指纹未知）。
	e.media.failURLs = map[string]error{
		"https://uiv.frp.musiver.cn/f/b2.png": errors.New("404 not found"),
	}
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 99, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-b", RoundNumber: 2,
		VisualScreensFingerprint: encodeScreensFingerprint(screensFingerprint{urlFP: urlFPA, contentFP: contentA}),
		SyncPending:              true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// B 轮开了视觉轮，账本指纹推进到 B 的 URL 集合：旧集合的指纹不再留在账
	// 本上，后续轮次拿它比对不会误命中。
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 99", repo)
	stored := parseScreensFingerprint(ledger.VisualScreensFingerprint)
	if stored != (screensFingerprint{urlFP: urlFPB}) {
		t.Fatalf("fingerprint = %+v (%q), want B 集合的裸 urlFP %s（内容维度未知）",
			stored, ledger.VisualScreensFingerprint, urlFPB)
	}

	// B 轮视觉通过（不同截图上的通过）。
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ? AND round_number = ?", model.ReviewKindVisual, "sha-b", 2).
		Updates(map[string]any{
			"status":      model.ReviewRunCompleted,
			"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
		})

	// C 轮（round 3）：截图换回 A 的 URL，代码干净且距上次视觉无 UI 改动。
	e.fake.SetPR(repo, 99, &githubapi.PullRequest{
		RepoFullName: repo, Number: 99, HeadSHA: "sha-c", Body: bodyA,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	runC := e.seedRun(t, repo, 99, "sha-c", model.ReviewRunCompleted, now.Add(time.Minute))
	e.svc.db.Model(runC).Updates(map[string]any{
		"round_number": 3, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 99).
		Updates(map[string]any{"head_sha": "sha-c", "round_number": 3})
	e.setLedgerPending(t, repo, 99)
	calls := e.media.anonCalls
	e.review.syncPendingPRs(now.Add(time.Minute))

	// C 轮：A 的 URL 不再命中账本指纹 → 探测发生，且不沿用 B 的 success——
	// 重开视觉轮（内容未知保守视为变化）。
	if e.media.anonCalls <= calls {
		t.Fatalf("anonCalls = %d, want > %d（换回 A 集合必须重新探测）", e.media.anonCalls, calls)
	}
	var round3 []model.ReviewRun
	e.svc.db.Where("kind = ? AND head_sha = ? AND round_number = ?", model.ReviewKindVisual, "sha-c", 3).Find(&round3)
	if len(round3) != 1 {
		t.Fatalf("round 3 visual runs = %d, want 1（A 的截图要重新审查，不沿用 B 的 success）", len(round3))
	}
	var cStatuses int
	for _, s := range e.fake.StatusesForContext(reviewVisualStatusContext) {
		if s.SHA == "sha-c" {
			cStatuses++
			if s.State != "pending" {
				t.Fatalf("status = %+v, want 明确的 pending（B 的通过不适用于 A 的截图，也不得沿用成 success）", s)
			}
		}
	}
	if cStatuses != 1 {
		t.Fatalf("sha-c 的视觉 status = %d 条, want 1（pending）", cStatuses)
	}
}

// E.3 ①：两张图只有一张内容变了（换了 URL 重传）→ 内容指纹不同 → 判变化
// → 开新视觉轮，不能沿用上一轮 success。
func TestVisualSyncOneScreenContentChangedReopens(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	shotX, shotY, shotY2 := writeShot(t, "px"), writeShot(t, "py"), writeShot(t, "py2")
	u1 := "https://uiv.frp.musiver.cn/f/one.png"
	u2 := "https://uiv.frp.musiver.cn/f/two.png"
	u3 := "https://uiv.frp.musiver.cn/f/two-v2.png"
	media := &perURLMedia{fakeReviewMedia: e.media, overrides: map[string]*githubmedia.CachedMedia{
		u1: shotX, u2: shotY, u3: shotY2,
	}}
	e.review = e.withT3(nil, media, nil)

	body1 := "![一](" + u1 + ") ![二](" + u2 + ")"
	body2 := "![一](" + u1 + ") ![二新](" + u3 + ")"
	contentFP1 := screenshotContentFingerprint([]string{shotX.Path, shotY.Path})
	contentFP2 := screenshotContentFingerprint([]string{shotX.Path, shotY2.Path})
	if contentFP1 == contentFP2 {
		t.Fatal("前置不成立：内容指纹应不同")
	}
	e.fake.SetPR(repo, 100, &githubapi.PullRequest{
		RepoFullName: repo, Number: 100, HeadSHA: "sha-oc", Body: body2,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-oc")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	prior := e.seedRun(t, repo, 100, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 100, "sha-oc", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 100, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-oc", RoundNumber: 2,
		VisualScreensFingerprint: encodeScreensFingerprint(screensFingerprint{
			urlFP:     screenshotFingerprint(collectScreenshotURLs(body1)),
			contentFP: contentFP1,
		}),
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// 一张内容变了：探测发生、ScreensChanged → 开新视觉轮（不沿用 success）。
	if media.anonCalls != 2 {
		t.Fatalf("anonCalls = %d, want 2（两张都要下载算内容指纹）", media.anonCalls)
	}
	var visualRuns []model.ReviewRun
	e.svc.db.Where("kind = ? AND head_sha = ?", model.ReviewKindVisual, "sha-oc").Find(&visualRuns)
	if len(visualRuns) != 1 {
		t.Fatalf("visual runs = %d, want 1（内容变了必须重审）", len(visualRuns))
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "pending" {
		t.Fatalf("statuses = %+v, want 一条 pending（开新轮）", statuses)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 100", repo)
	wantFP := encodeScreensFingerprint(screensFingerprint{
		urlFP: screenshotFingerprint(collectScreenshotURLs(body2)), contentFP: contentFP2,
	})
	if ledger.VisualScreensFingerprint != wantFP {
		t.Fatalf("fingerprint = %q, want %q（新集合 + 新内容入账）", ledger.VisualScreensFingerprint, wantFP)
	}
}

// E.3 ②：全部下载成功、字节相同、引用顺序颠倒（URL 集合换了）→ 内容指纹
// 与顺序无关 → 判未变化 → 沿用 success，不重开轮。
func TestVisualSyncReversedRefOrderSameContentCarries(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	shotX, shotY := writeShot(t, "qx"), writeShot(t, "qy")
	u1, u2 := "https://uiv.frp.musiver.cn/f/one.png", "https://uiv.frp.musiver.cn/f/two.png"
	u3, u4 := "https://uiv.frp.musiver.cn/f/one-r.png", "https://uiv.frp.musiver.cn/f/two-r.png"
	media := &perURLMedia{fakeReviewMedia: e.media, overrides: map[string]*githubmedia.CachedMedia{
		u1: shotX, u2: shotY, u3: shotX, u4: shotY,
	}}
	e.review = e.withT3(nil, media, nil)

	body1 := "![一](" + u1 + ") ![二](" + u2 + ")"
	body2 := "![二](" + u4 + ") ![一](" + u3 + ")"
	contentFP := screenshotContentFingerprint([]string{shotX.Path, shotY.Path})
	e.fake.SetPR(repo, 101, &githubapi.PullRequest{
		RepoFullName: repo, Number: 101, HeadSHA: "sha-ro", Body: body2,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-ro")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	prior := e.seedRun(t, repo, 101, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 101, "sha-ro", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 101, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-ro", RoundNumber: 2,
		VisualScreensFingerprint: encodeScreensFingerprint(screensFingerprint{
			urlFP:     screenshotFingerprint(collectScreenshotURLs(body1)),
			contentFP: contentFP,
		}),
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// URL 集合换了所以要探测，但字节相同且排序后与顺序无关 → 内容未变 →
	// 沿用 success，不重开轮。
	if media.anonCalls != 2 {
		t.Fatalf("anonCalls = %d, want 2（URL 集合变了要探测）", media.anonCalls)
	}
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ?", model.ReviewKindVisual, "sha-ro").Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（内容相同不重开轮）", visual)
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "success" ||
		!strings.Contains(statuses[0].Description, "沿用上一轮，截图未变化") {
		t.Fatalf("statuses = %+v, want 沿用 success（内容指纹与引用顺序无关）", statuses)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 101", repo)
	wantFP := encodeScreensFingerprint(screensFingerprint{
		urlFP: screenshotFingerprint(collectScreenshotURLs(body2)), contentFP: contentFP,
	})
	if ledger.VisualScreensFingerprint != wantFP {
		t.Fatalf("fingerprint = %q, want %q（新 URL 集合 + 相同内容入账）", ledger.VisualScreensFingerprint, wantFP)
	}
}

// visualStatusFailClient 让「视觉审查」context 的 commit status 首次写入失败
// （模拟 GitHub 5xx/网络故障），其余 context 原样透传——代码状态、评论不受
// 影响。错误只注入一次：重试走同一 client 不再失败。
type visualStatusFailClient struct {
	githubapi.GitHubClient
	done bool
}

func (c *visualStatusFailClient) CreateCommitStatus(ctx context.Context, inst int64, repo, sha, state, sctx, desc, url string) error {
	if sctx == reviewVisualStatusContext && !c.done {
		c.done = true
		return errors.New("502 bad gateway")
	}
	return c.GitHubClient.CreateCommitStatus(ctx, inst, repo, sha, state, sctx, desc, url)
}

// 修 2（审查构造的故障场景）：视觉运行已建但紧随的 GitHub pending 写失败时，
// 账本指纹必须在建运行的同一事务里落账——若只靠后面的 updateLedgerEpoch，
// 失败返回后重试会因「视觉运行已存在」直接走汇总分支、不再补指纹，账本停
// 留在更早轮的指纹上；之后换回旧 URL 集合的 head 会被误判「截图未变化」，
// 把不适用轮次的 success 写出去。
func TestVisualSyncFingerprintStoredWhenGitHubPendingWriteFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	bodyA := "![甲](https://uiv.frp.musiver.cn/f/a.png)"
	bodyB := "![乙一](https://uiv.frp.musiver.cn/f/b1.png) ![乙二](https://uiv.frp.musiver.cn/f/b2.png)"
	urlFPA := screenshotFingerprint(collectScreenshotURLs(bodyA))
	urlFPB := screenshotFingerprint(collectScreenshotURLs(bodyB))
	if urlFPA == urlFPB {
		t.Fatal("前置不成立：A/B 两个 URL 集合的指纹应不同")
	}
	contentA := screenshotContentFingerprint([]string{e.media.cached.Path})

	e.fake.SetPR(repo, 102, &githubapi.PullRequest{
		RepoFullName: repo, Number: 102, HeadSHA: "sha-gb", Body: bodyB,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-gb")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// A 轮（round 1）视觉通过，账本存 A 的完整指纹（URL + 内容）。
	priorA := e.seedRun(t, repo, 102, "sha-a", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(priorA).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	// B 轮（round 2）代码干净且距上次视觉有 UI 改动 → 走 visualStart；B 的第
	// 二张图恒失败 → 部分下载成功开轮，内容指纹未知（落账退化为裸 urlFP）。
	runB := e.seedRun(t, repo, 102, "sha-gb", model.ReviewRunCompleted, now)
	e.svc.db.Model(runB).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
	})
	e.media.failURLs = map[string]error{
		"https://uiv.frp.musiver.cn/f/b2.png": errors.New("404 not found"),
	}
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 102, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-gb", RoundNumber: 2,
		VisualScreensFingerprint: encodeScreensFingerprint(screensFingerprint{urlFP: urlFPA, contentFP: contentA}),
		SyncPending:              true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	client := &visualStatusFailClient{GitHubClient: e.fake}
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: client},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	// 前置确认同步确实失败：GitHub 写错误被记成 sync_error，pending 保留给重试。
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 102", repo)
	if ledger.SyncError == "" || !ledger.SyncPending {
		t.Fatalf("sync_error=%q pending=%v, want 同步失败且保留重试", ledger.SyncError, ledger.SyncPending)
	}
	// ① 视觉运行已建（GitHub 写失败不回滚建轮）。
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ? AND head_sha = ?",
		repo, 102, model.ReviewKindVisual, "sha-gb").Find(&visualRuns)
	if len(visualRuns) != 1 {
		t.Fatalf("visual runs = %d, want 1", len(visualRuns))
	}
	// ② 账本指纹已随运行推进到 B 的 URL 集合——不能停留在 A 的旧指纹上。
	if stored := parseScreensFingerprint(ledger.VisualScreensFingerprint); stored != (screensFingerprint{urlFP: urlFPB}) {
		t.Fatalf("fingerprint = %+v (%q), want B 集合的裸 urlFP %s（与运行同事务落账）",
			stored, ledger.VisualScreensFingerprint, urlFPB)
	}
	// ③ 视觉状态没有 success（pending 写失败，也不得沿用 A 轮的通过）。
	for _, s := range e.fake.StatusesForContext(reviewVisualStatusContext) {
		if s.SHA == "sha-gb" && s.State == "success" {
			t.Fatalf("status = %+v, want 不写 success", s)
		}
	}

	// 同一 client（错误只注入一次）重试：同一 epoch 的失败重试有
	// reviewSyncRetryBackoff 退避，要跳过窗口；视觉运行已存在，直接走汇总
	// 分支补写 pending——仍不得写 success，指纹也不被覆盖。
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	var gbStatuses int
	for _, s := range e.fake.StatusesForContext(reviewVisualStatusContext) {
		if s.SHA == "sha-gb" {
			gbStatuses++
			if s.State != "pending" {
				t.Fatalf("status = %+v, want 重试补写 pending（不沿用 A 的 success）", s)
			}
		}
	}
	if gbStatuses != 1 {
		t.Fatalf("sha-gb 的视觉 status = %d 条, want 1（pending）", gbStatuses)
	}
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 102", repo)
	if stored := parseScreensFingerprint(ledger.VisualScreensFingerprint); stored != (screensFingerprint{urlFP: urlFPB}) {
		t.Fatalf("fingerprint = %+v (%q), want 重试后仍为 B 的裸 urlFP", stored, ledger.VisualScreensFingerprint)
	}
}

// 修 1 的边界（复审 round 3）：决策时读到的视觉组合在探测期间被清空（启用状态
// 允许 0 个视觉组合，保存设置不推进账本 epoch）时，事务内建不出任何运行——
// 此时绝不能落账指纹，否则账本指向一次从未发生的审查，之后同一批截图的新
// head 会因指纹相同被判「未变化」，把更早一轮的通过沿用过去。
func TestCreateVisualRunsSkipsFingerprintWhenNoCombos(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	emptyCombos, err := marshalCombinations(nil)
	if err != nil {
		t.Fatalf("marshal empty combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = emptyCombos
	})
	oldFP := encodeScreensFingerprint(screensFingerprint{urlFP: "aaaaaaaaaaaaaaaa", contentFP: "bbbbbbbbbbbbbbbb"})
	ledger := &model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 103, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-nc", RoundNumber: 2,
		VisualScreensFingerprint: oldFP, SyncEpoch: 3, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	pr := &githubapi.PullRequest{
		RepoFullName: repo, Number: 103, HeadSHA: "sha-nc",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	}
	newFP := encodeScreensFingerprint(screensFingerprint{urlFP: "cccccccccccccccc"})
	runs, aborted, err := e.review.createVisualRuns(ledger, pr, nil, now, newFP)
	if err != nil || aborted {
		t.Fatalf("createVisualRuns = (%v, aborted=%v, %v), want 无错误、未 aborted", runs, aborted, err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %d, want 0（无视觉组合不建运行）", len(runs))
	}
	var cur model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 103).First(&cur).Error; err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	if cur.VisualScreensFingerprint != oldFP {
		t.Fatalf("fingerprint = %q, want %q（没建出运行就不动指纹）", cur.VisualScreensFingerprint, oldFP)
	}
}

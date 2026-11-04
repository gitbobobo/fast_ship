package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// AST 收集口径的回归：来源形态（引用式/尖括号目的地/HTML 实体/注释/代码）、
// 显式 URL 不裁尾、裸链尾部非 GFM 标点兜底。

// 引用式图片（![a][id] + 定义行）由 goldmark 填充 Destination，照常收集；
// 没有定义的引用不产 Image 节点，不收集。
func TestCollectScreenshotURLsReferenceStyle(t *testing.T) {
	refs := collectScreenshotURLs("见 ![界面截图][shot] 与 ![附件][att]\n\n" +
		"[shot]: https://uiv.example.cn/f/x.png\n" +
		"[att]: https://github.com/user-attachments/assets/uuid")
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 引用式图片按文档序收集 2 条", refs)
	}
	if refs[0].URL != "https://uiv.example.cn/f/x.png" || refs[0].Label != "界面截图" {
		t.Fatalf("refs[0] = %+v, want 定义行 URL + alt 标签", refs[0])
	}
	if refs[1].URL != "https://github.com/user-attachments/assets/uuid" {
		t.Fatalf("refs[1] = %+v, want 附件定义 URL", refs[1])
	}
	// 未定义的引用保持字面文本，没有 Image 节点。
	if refs := collectScreenshotURLs("![缺][nope] 没有定义"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want 未定义引用不收集", refs)
	}
}

// 尖括号目的地（![a](<u(1).png>)）的括号配对：括号是 URL 的一部分，收集原
// 样保留。显式声明的 URL 一律不裁尾（?token=abc! 结尾的 ! 是查询串的一部分，
// 裁掉会 404）。
func TestCollectScreenshotURLsExplicitDestinationUntouched(t *testing.T) {
	refs := collectScreenshotURLs("![a](<https://uiv.example.cn/f/u(1).png>)")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/u(1).png" {
		t.Fatalf("refs = %+v, want 尖括号目的地的配对括号原样保留", refs)
	}
	refs = collectScreenshotURLs("![a](https://uiv.example.cn/f/x.png?token=abc!)")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/x.png?token=abc!" {
		t.Fatalf("refs = %+v, want 显式 URL 不裁尾（! 属于查询串）", refs)
	}
	refs = collectScreenshotURLs("<img src=\"https://uiv.example.cn/f/x.png?token=abc!\">")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/x.png?token=abc!" {
		t.Fatalf("refs = %+v, want <img> 的显式 URL 同样不裁尾", refs)
	}
}

// HTML <img> 用 x/net/html 解析：实体由解析器解码（&amp; → &），注释里的
// img 不产元素节点。
func TestCollectScreenshotURLsHTMLImgEntityAndComment(t *testing.T) {
	refs := collectScreenshotURLs("看 <img src=\"https://uiv.example.cn/f/a.png?a=1&amp;b=2\"> 结束")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/a.png?a=1&b=2" {
		t.Fatalf("refs = %+v, want HTML src 实体解码后收集", refs)
	}
	// 整行 <img> 是 HTML 块，同样解析。
	refs = collectScreenshotURLs("<img src='https://uiv.example.cn/f/a.png?a=1&amp;b=2'>\n\n正文")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/a.png?a=1&b=2" {
		t.Fatalf("refs = %+v, want HTML 块里的 img 同样解码收集", refs)
	}
	// HTML 注释（整行与行内）不产 img 元素，不收集。
	for _, body := range []string{
		"<!-- <img src=\"https://uiv.example.cn/f/c.png\"> -->",
		"正文 <!-- https://uiv.example.cn/f/c.png --> 继续",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 0 {
			t.Fatalf("body %q: refs = %+v, want 注释内容不收集", body, refs)
		}
	}
}

// 跨行 <img>：HTMLBlock 整块拼接后解析，标签拆开多行照样收集（逐行解析会
// 把每个 fragment 都看成不完整元素而漏掉）。
func TestCollectScreenshotURLsMultilineImg(t *testing.T) {
	refs := collectScreenshotURLs("<div>\n<img\n  src=\"https://uiv.example.cn/f/shot.png\">\n</div>\n\n正文")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/shot.png" {
		t.Fatalf("refs = %+v, want 跨行 <img> 收集到完整 src", refs)
	}
}

// 带行内格式的 alt：文本套在 Emphasis 等节点下，递归收集后代文本做 label。
func TestCollectScreenshotURLsFormattedAltLabel(t *testing.T) {
	refs := collectScreenshotURLs("![**登录页** v1](https://uiv.example.cn/f/a.png)")
	if len(refs) != 1 || refs[0].Label != "登录页 v1" {
		t.Fatalf("refs = %+v, want 格式化 alt 拼成 label", refs)
	}
}

// Markdown 目的地不解码实体是 CommonMark 行为（goldmark 原样保留 &amp;），
// 与 HTML 来源（解码）口径不同——接受这个边界，标签/指纹都按收集结果算。
func TestCollectScreenshotURLsMarkdownEntityBoundary(t *testing.T) {
	refs := collectScreenshotURLs("![a](https://uiv.example.cn/f/a.png?a=1&amp;b=2)")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/a.png?a=1&amp;b=2" {
		t.Fatalf("refs = %+v, want Markdown 目的地实体原样保留（CommonMark 行为）", refs)
	}
}

// 裸链尾部的非 GFM 标点由兜底裁剪剥掉：Linkify 只剥它规范里的集合，中文
// 句读、书名号、下划线等由 trimTrailingPunct 兜底——不裁会破坏扩展名判定
// （漏收）或让附件直链 404（误写「截图无法下载」）。
func TestCollectScreenshotURLsBareTrailingNonGFM(t *testing.T) {
	const u = "https://uiv.example.cn/f/a.png"
	for _, p := range []string{"_", "~", "'", "》", "』", "」", "，", "、", "；", "：", "…"} {
		refs := collectScreenshotURLs("see " + u + p + " tail")
		if len(refs) != 1 || refs[0].URL != u {
			t.Fatalf("尾标点 %q: refs = %+v, want 兜底裁剪后收集", p, refs)
		}
	}
	// 括号配对兜底：`)` 多于 `(` 时剥尾部多余 `)`，配对括号保留。
	refs := collectScreenshotURLs("(see https://uiv.example.cn/f/a(1).png) end")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/a(1).png" {
		t.Fatalf("refs = %+v, want 配对括号保留、多余右括号剥掉", refs)
	}
}

// 普通链接目的地回归：旧 bareAttachRe 会收 `[label](github-attach-url)` 与
// <a href> 里的附件直链——AST 走 ast.Link / a[href] 补齐，准入同裸链口径
// （GitHub 附件形态 ∪ 位图扩展名），普通网页链接不误收。
func TestCollectScreenshotURLsMarkdownAndAnchorLinks(t *testing.T) {
	attach := "https://github.com/user-attachments/assets/9f0c8b7a-1111-2222-3333-444455556666"
	refs := collectScreenshotURLs("见 [附件](" + attach + ") 与 <a href=\"" + attach + "?v=2\">大图</a>")
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 链接目的地（md + a[href]）都收集", refs)
	}
	if refs[0].URL != attach || refs[0].Label != "附件" {
		t.Fatalf("refs[0] = %+v, want md 链接目的地 + label 文本", refs[0])
	}
	if refs[1].URL != attach+"?v=2" {
		t.Fatalf("refs[1] = %+v, want <a href> 目的地", refs[1])
	}
	// 位图扩展名的普通链接也收（链接语义下它多半就是图）。
	refs = collectScreenshotURLs("[界面](https://uiv.example.cn/f/shot.png)")
	if len(refs) != 1 || refs[0].URL != "https://uiv.example.cn/f/shot.png" || refs[0].Label != "界面" {
		t.Fatalf("refs = %+v, want 位图扩展名链接收集 + label", refs)
	}
	// 无图片形状兜底的普通链接不收——否则描述里每个网页链接都成截图。
	for _, body := range []string{
		"[文档](https://example.com/docs/page)",
		"<a href=\"https://example.com/docs/page\">文档</a>",
		"[www 站](https://example.com/)",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 0 {
			t.Fatalf("body %q: refs = %+v, want 普通网页链接不收集", body, refs)
		}
	}
}

// www. 前缀裸链：Linkify 按 GFM 补 http:// scheme（作者没写协议），收集时
// 升级 https 进准入——否则 GitHub 渲染成可点链接的图地址被我们静默丢掉。
// 显式写 http:// 的裸链不升级，准入照常拒收。
func TestCollectScreenshotURLsWWWPrefixUpgrade(t *testing.T) {
	refs := collectScreenshotURLs("see www.uiv.example.cn/f/a.png end")
	if len(refs) != 1 || refs[0].URL != "https://www.uiv.example.cn/f/a.png" {
		t.Fatalf("refs = %+v, want www. 裸链升级 https 收集", refs)
	}
	if refs := collectScreenshotURLs("see http://uiv.example.cn/f/a.png end"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want 显式 http:// 裸链仍被拒收", refs)
	}
}

// 探测三态：全部「不是图」→ probeNone（收敛 visualNone，不刷「截图无法下
// 载」）；「不是图」与「拉不到」混合 → probeUnavailable 且 reason 取拉不到
// 那条。
func TestProbeScreenshotsClassifiesNotAnImage(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	badge := "https://img.shields.io/badge/x.svg"
	png := "https://uiv.example.cn/f/ok.png"
	body := "![b](" + badge + ") ![p](" + png + ")"

	// 全部「不是图」→ probeNone。
	e.media.failURLs = map[string]error{badge: githubmedia.ErrUnsupportedUpstreamMedia}
	e.media.anonErr = githubmedia.ErrUnsupportedUpstreamMedia
	probe, _ := review.probeScreenshots(context.Background(), model.ForgeIntegrationBuiltinGitHubID, 1, body)
	if probe.result != probeNone {
		t.Fatalf("probe = %+v, want probeNone（全部不是图）", probe)
	}

	// 混合：徽章不是图 + png 拉不到 → probeUnavailable，原因取「拉不到」。
	e.media.anonErr = nil
	e.media.failURLs = map[string]error{
		badge: githubmedia.ErrUnsupportedUpstreamMedia,
		png:   errors.New("connection refused"),
	}
	probe, _ = review.probeScreenshots(context.Background(), model.ForgeIntegrationBuiltinGitHubID, 1, body)
	if probe.result != probeUnavailable {
		t.Fatalf("probe = %+v, want probeUnavailable（存在拉不到）", probe)
	}
	if probe.reason != "无法连接图片服务器" {
		t.Fatalf("reason = %q, want 拉不到那条的原因", probe.reason)
	}

	// 形态不支持的地址（ErrInvalidMediaURL）也算「不是图」。
	e.media.failURLs = map[string]error{
		badge: githubmedia.ErrInvalidMediaURL,
		png:   githubmedia.ErrInvalidMediaURL,
	}
	probe, _ = review.probeScreenshots(context.Background(), model.ForgeIntegrationBuiltinGitHubID, 1, body)
	if probe.result != probeNone {
		t.Fatalf("probe = %+v, want probeNone（形态不支持=不是图）", probe)
	}
}

// 描述删光了截图：HasScreenshots=false → visualNone，本轮不写任何视觉状态
// （上一轮的通过不是本 head 的结论，不能沿用——描述里已经没有截图可复核
// 了），只把「空集合已结清」的指纹入账，同一集合后续事件不再重探。
func TestVisualSyncCarryMessageWithoutScreenshots(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	oldBody := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 93, &githubapi.PullRequest{
		RepoFullName: repo, Number: 93, HeadSHA: "sha-cg", Body: oldBody,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-cg")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉通过。
	prior := e.seedRun(t, repo, 93, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 93, "sha-cg", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(oldBody))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 93, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cg", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	// 描述删光了截图：HasScreenshots=false → visualNone，不写视觉状态。
	e.fake.SetPR(repo, 93, &githubapi.PullRequest{
		RepoFullName: repo, Number: 93, HeadSHA: "sha-cg",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: "只改了文档",
	})
	e.review.syncPendingPRs(now)

	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 0 {
		t.Fatalf("statuses = %+v, want 0（无截图不沿用上一轮结论）", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 93", repo)
	if want := settledNonePrefixV2 + screenshotFingerprint(nil); ledger.VisualScreensFingerprint != want {
		t.Fatalf("fingerprint = %q, want %q（空集合以结清态入账）", ledger.VisualScreensFingerprint, want)
	}
	// 没有截图引用就不探测。
	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（无截图不探测）", e.media.anonCalls)
	}
	// 同一空集合再来一次同步事件：不重复探测（结清态收敛）。
	e.setLedgerPending(t, repo, 93)
	e.review.syncPendingPRs(now.Add(time.Minute))
	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（结清态不重探）", e.media.anonCalls)
	}
}

// 另一路：截图引用还在但全部「不是图」（probeNone 改判 HasScreenshots=false）
// → 同样 visualNone 不写视觉状态，集合以结清态入账（同一批徽章地址不再重探）。
func TestVisualSyncCarryMessageWhenAllNotImages(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	oldBody := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	newBody := "![badge](https://img.shields.io/badge/x.svg)"
	e.fake.SetPR(repo, 94, &githubapi.PullRequest{
		RepoFullName: repo, Number: 94, HeadSHA: "sha-cm", Body: newBody,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-cm")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	prior := e.seedRun(t, repo, 94, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 94, "sha-cm", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	// 账本指纹是旧描述的：新描述只有徽章 → 集合指纹不同 → 探测真的发生。
	fp := screenshotFingerprint(collectScreenshotURLs(oldBody))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 94, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cm", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.media.failURLs = map[string]error{
		"https://img.shields.io/badge/x.svg": githubmedia.ErrUnsupportedUpstreamMedia,
	}
	e.review.syncPendingPRs(now)

	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 0 {
		t.Fatalf("statuses = %+v, want 0（全部不是图不开视觉轮也不沿用结论）", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 94", repo)
	if want := settledNonePrefixV2 + screenshotFingerprint(collectScreenshotURLs(newBody)); ledger.VisualScreensFingerprint != want {
		t.Fatalf("fingerprint = %q, want %q（徽章集合以结清态入账）", ledger.VisualScreensFingerprint, want)
	}
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("kind = ? AND head_sha = ?", model.ReviewKindVisual, "sha-cm").Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（全部不是图不开视觉轮）", visual)
	}
	// 同一徽章集合再来一次同步事件：不再重探。
	e.setLedgerPending(t, repo, 94)
	callsBefore := e.media.anonCalls
	e.review.syncPendingPRs(now.Add(time.Minute))
	if e.media.anonCalls != callsBefore {
		t.Fatalf("anonCalls %d → %d, want 结清态不重探", callsBefore, e.media.anonCalls)
	}
}

// probeNone 的收敛：全部「不是图」判定后指纹入账——后续同步事件（含手动
// 重跑）ScreensChanged=false 不再重探这些地址；集合变化（指纹变）自然恢复。
func TestVisualSyncProbeNoneSettlesFingerprint(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	badge := "https://img.shields.io/badge/x.svg"
	newBody := "![badge](" + badge + ")"
	e.fake.SetPR(repo, 96, &githubapi.PullRequest{
		RepoFullName: repo, Number: 96, HeadSHA: "sha-pn", Body: newBody,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-pn")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	run := e.seedRun(t, repo, 96, "sha-pn", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 1, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
	})
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 96, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-pn", RoundNumber: 1,
		// 账本指纹是旧集合的：新描述只有徽章 → 指纹不同 → 探测发生。
		VisualScreensFingerprint: "old-set-fp", SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	e.media.failURLs = map[string]error{badge: githubmedia.ErrUnsupportedUpstreamMedia}

	e.review.syncPendingPRs(now)

	var cur model.ReviewPullRequest
	if err := e.svc.db.First(&cur, "project_id = ? AND repo_full_name = ? AND pr_number = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).Error; err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	want := settledNonePrefixV2 + screenshotFingerprint(collectScreenshotURLs(newBody))
	if cur.VisualScreensFingerprint != want {
		t.Fatalf("fingerprint = %q, want %q（全部不是图判定后以结清态入账）", cur.VisualScreensFingerprint, want)
	}
	if e.media.anonCalls == 0 {
		t.Fatal("第一次同步应真实探测过")
	}

	// 再来一次同步事件（同一截图集合）：探测不再发生。
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
		Updates(map[string]any{"sync_pending": true, "sync_epoch": 2}).Error; err != nil {
		t.Fatalf("re-arm ledger: %v", err)
	}
	callsBefore := e.media.anonCalls
	e.review.syncPendingPRs(now.Add(time.Minute))
	if e.media.anonCalls != callsBefore {
		t.Fatalf("anonCalls %d → %d, want 同一集合不重探", callsBefore, e.media.anonCalls)
	}
}

// scrubLogURLs：日志文本里的 http(s) URL 剥 query/fragment/userinfo，正文
// 与解析不了的序列原样保留。
func TestScrubLogURLs(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			// *url.Error 的内嵌 URL：预签名 query 必须剥掉。
			"url.Error 剥 query",
			`Get "https://uiv.example.cn/f/x.png?sig=abc&exp=1": dial tcp: connection refused`,
			`Get "https://uiv.example.cn/f/x.png": dial tcp: connection refused`,
		},
		{
			"userinfo 剥掉",
			"https://user:secret@uiv.example.cn/f/x.png 上传失败",
			"https://uiv.example.cn/f/x.png 上传失败",
		},
		{
			"fragment 剥掉",
			"https://uiv.example.cn/docs/page#section 读取失败",
			"https://uiv.example.cn/docs/page 读取失败",
		},
		{
			"干净 URL 原样",
			"下载 https://uiv.example.cn/f/x.png 失败",
			"下载 https://uiv.example.cn/f/x.png 失败",
		},
		{
			"无 URL 文本原样",
			"无法连接图片服务器",
			"无法连接图片服务器",
		},
		{
			"重定向后的预签名地址剥 query",
			`Get "https://s3.amazonaws.com/bucket/x.png?X-Amz-Signature=tok": response 403`,
			`Get "https://s3.amazonaws.com/bucket/x.png": response 403`,
		},
	}
	for _, c := range cases {
		if got := scrubLogURLs(c.in); got != c.want {
			t.Fatalf("%s: scrubLogURLs = %q, want %q", c.name, got, c.want)
		}
	}
}

// AgentScreenshots 的陈旧行过滤：SourceURL 不在当前描述收集结果里的资产行
// 不再给审查员（旧口径的规整残留行）。
func TestAgentScreenshotsFiltersStaleRows(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 95, &githubapi.PullRequest{
		RepoFullName: repo, Number: 95, HeadSHA: "sha-st",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-st")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})
	visualRun := e.seedRun(t, repo, 95, "sha-st", model.ReviewRunQueued, now)
	e.svc.db.Model(visualRun).Update("kind", model.ReviewKindVisual)

	// 当前 URL 的行 + 一个旧口径残留行（URL 已不在当前描述里）。
	current := &model.ReviewAsset{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            "asset-current", RepoFullName: repo, PRNumber: 95, HeadSHA: "sha-st",
		Position: 0, Label: "界面", SourceURL: "https://uiv.frp.musiver.cn/f/x.png",
		StoragePath: "/cache/x.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now,
	}
	stale := &model.ReviewAsset{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            "asset-stale", RepoFullName: repo, PRNumber: 95, HeadSHA: "sha-st",
		Position: 1, Label: "旧图", SourceURL: "https://uiv.frp.musiver.cn/f/old-uiv.png",
		StoragePath: "/cache/old.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now,
	}
	for _, a := range []*model.ReviewAsset{current, stale} {
		if err := e.svc.db.Create(a).Error; err != nil {
			t.Fatalf("seed asset: %v", err)
		}
	}

	items, _, err := e.review.AgentScreenshots(context.Background(), visualRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if len(items) != 1 || items[0].Id != "asset-current" {
		t.Fatalf("items = %+v, want 只剩当前描述里的资产行", items)
	}
}

// 张数截断：超过 maxScreenshotsPerPR 的引用按出现顺序保留前 N 个。review
// 截图面回收滞后至少 30 天（行账保留策略），收集面是磁盘占用的第一道闸——
// 探测字节预算与下载面单文件上限都在它之后。
func TestCollectScreenshotURLsTruncatesAtLimit(t *testing.T) {
	var lines []string
	for i := 0; i < maxScreenshotsPerPR+8; i++ {
		lines = append(lines, fmt.Sprintf("![s%d](https://uiv.example.com/f/%d.png)", i, i))
	}
	refs := collectScreenshotURLsAt(strings.Join(lines, "\n\n"), "")
	if len(refs) != maxScreenshotsPerPR {
		t.Fatalf("len(refs) = %d, want %d（超出部分忽略）", len(refs), maxScreenshotsPerPR)
	}
	for i, ref := range refs {
		want := fmt.Sprintf("https://uiv.example.com/f/%d.png", i)
		if ref.URL != want {
			t.Fatalf("refs[%d] = %s, want %s（按出现顺序保留靠前的引用）", i, ref.URL, want)
		}
	}
}

// changed 两侧口径必须一致：本轮指纹来自截断后的收集结果，上一轮的行可能
// 是升级前留下的全量（同一 head 上超过张数上限）。不按同一规则截断，多出来
// 的旧 URL 会让指纹恒不相等，changed 永远为 true。
func TestAgentScreenshotsChangedTruncatesPrevAssets(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	base := "https://uiv.frp.musiver.cn/f/"
	var lines []string
	for i := 0; i < maxScreenshotsPerPR; i++ {
		lines = append(lines, fmt.Sprintf("![s%d](%s%d.png)", i, base, i))
	}
	body := strings.Join(lines, "\n\n")
	e.fake.SetPR(repo, 96, &githubapi.PullRequest{
		RepoFullName: repo, Number: 96, HeadSHA: "sha-cur",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-c")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	prevRun := e.seedRun(t, repo, 96, "sha-prev", model.ReviewRunCompleted, now)
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now})
	curRun := e.seedRun(t, repo, 96, "sha-cur", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	mk := func(id, head, url string, pos int) *model.ReviewAsset {
		return &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 96, HeadSHA: head,
			Position: pos, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: now,
		}
	}
	// 上一轮 head：张数上限内的 12 张 + 1 张超限残留（升级前写入的全量）。
	for i := 0; i < maxScreenshotsPerPR; i++ {
		if err := e.svc.db.Create(mk(fmt.Sprintf("prev-%d", i), "sha-prev", fmt.Sprintf("%s%d.png", base, i), i)).Error; err != nil {
			t.Fatalf("seed prev asset: %v", err)
		}
	}
	if err := e.svc.db.Create(mk("prev-extra", "sha-prev", base+"extra.png", maxScreenshotsPerPR)).Error; err != nil {
		t.Fatalf("seed prev extra: %v", err)
	}
	// 当前 head：就是描述里的 12 张。
	for i := 0; i < maxScreenshotsPerPR; i++ {
		if err := e.svc.db.Create(mk(fmt.Sprintf("cur-%d", i), "sha-cur", fmt.Sprintf("%s%d.png", base, i), i)).Error; err != nil {
			t.Fatalf("seed cur asset: %v", err)
		}
	}

	items, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if len(items) != maxScreenshotsPerPR {
		t.Fatalf("items = %d, want %d", len(items), maxScreenshotsPerPR)
	}
	if changed {
		t.Fatal("changed = true, want false（上一轮超限残留不应改变指纹）")
	}
}

// 上一轮的行里可能混有今天的口径不会再收的残留（旧版本写入的 http 地址、
// 带 userinfo 的链接）。它们从未展示给审查员，却会改变指纹让 changed 恒为
// true——比较前按收集面同一道结构准入过滤。
func TestAgentScreenshotsChangedDropsUnadmittedPrevRows(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	base := "https://uiv.frp.musiver.cn/f/"
	var lines []string
	for i := 0; i < maxScreenshotsPerPR; i++ {
		lines = append(lines, fmt.Sprintf("![s%d](%s%d.png)", i, base, i))
	}
	body := strings.Join(lines, "\n\n")
	e.fake.SetPR(repo, 97, &githubapi.PullRequest{
		RepoFullName: repo, Number: 97, HeadSHA: "sha-cur",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-u")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	prevRun := e.seedRun(t, repo, 97, "sha-prev", model.ReviewRunCompleted, now)
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now})
	curRun := e.seedRun(t, repo, 97, "sha-cur", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	create := func(id, head, url string, pos int) {
		t.Helper()
		if err := e.svc.db.Create(&model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 97, HeadSHA: head,
			Position: pos, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed asset: %v", err)
		}
	}

	// 上一轮：position 0/1 是旧口径残留（今天的准入不会收），后面才是真正
	// 展示过的 12 张。只按 position 截断会把这 12 张挤掉两张，指纹必然不同。
	create("prev-bad-0", "sha-prev", "http://uiv.frp.musiver.cn/f/plain.png", 0)
	create("prev-bad-1", "sha-prev", "https://user:pw@uiv.frp.musiver.cn/f/userinfo.png", 1)
	for i := 0; i < maxScreenshotsPerPR; i++ {
		create(fmt.Sprintf("prev-%d", i), "sha-prev", fmt.Sprintf("%s%d.png", base, i), i+2)
		create(fmt.Sprintf("cur-%d", i), "sha-cur", fmt.Sprintf("%s%d.png", base, i), i)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false（旧口径残留不该改变指纹）")
	}
}

// 旧版本写入的行可能带 #fragment（规整规则后来才落地），而结构准入不拒
// fragment——比较前不规整，指纹与当前集合恒不相等，changed 永远为 true。
func TestAgentScreenshotsChangedNormalizesPrevFragments(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	base := "https://uiv.frp.musiver.cn/f/"
	var lines []string
	for i := 0; i < 3; i++ {
		lines = append(lines, fmt.Sprintf("![s%d](%s%d.png)", i, base, i))
	}
	body := strings.Join(lines, "\n\n")
	e.fake.SetPR(repo, 98, &githubapi.PullRequest{
		RepoFullName: repo, Number: 98, HeadSHA: "sha-cur",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-f")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	prevRun := e.seedRun(t, repo, 98, "sha-prev", model.ReviewRunCompleted, now)
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now})
	curRun := e.seedRun(t, repo, 98, "sha-cur", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	create := func(id, head, url string, pos int) {
		t.Helper()
		if err := e.svc.db.Create(&model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 98, HeadSHA: head,
			Position: pos, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed asset: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		// 上一轮的行带 fragment，当前描述里的没有。
		create(fmt.Sprintf("prev-%d", i), "sha-prev", fmt.Sprintf("%s%d.png#frag", base, i), i)
		create(fmt.Sprintf("cur-%d", i), "sha-cur", fmt.Sprintf("%s%d.png", base, i), i)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false（fragment 规整后两侧应一致）")
	}
}

// 同一 head 的 manual rerun：ensureAssets 在比较之前已把本轮新收集的 URL 写
// 进同一张共享资产表。不过滤「本轮开始之后才建的行」，新增截图会被算进上一
// 轮——两侧指纹相等，changed 误判为 false，新增的那张不会被提示。
func TestAgentScreenshotsChangedExcludesRowsFromThisRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	base := "https://uiv.frp.musiver.cn/f/"
	var lines []string
	for i := 0; i < maxScreenshotsPerPR-1; i++ {
		lines = append(lines, fmt.Sprintf("![s%d](%s%d.png)", i, base, i))
	}
	lines = append(lines, fmt.Sprintf("![new](%snew.png)", base))
	body := strings.Join(lines, "\n\n")
	e.fake.SetPR(repo, 99, &githubapi.PullRequest{
		RepoFullName: repo, Number: 99, HeadSHA: "sha-same",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-r")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	// 两轮同 head：上一轮只见过前 11 张，本轮描述新增了第 12 张。
	prevRun := e.seedRun(t, repo, 99, "sha-same", model.ReviewRunCompleted, now)
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now})
	curRun := e.seedRun(t, repo, 99, "sha-same", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	for i := 0; i < maxScreenshotsPerPR-1; i++ {
		if err := e.svc.db.Create(&model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ID:            fmt.Sprintf("prev-%d", i),
			RepoFullName:  repo, PRNumber: 99, HeadSHA: "sha-same",
			Position: i, Label: fmt.Sprintf("s%d", i),
			SourceURL:   fmt.Sprintf("%s%d.png", base, i),
			StoragePath: fmt.Sprintf("/cache/prev-%d.png", i), MimeType: "image/png", SizeBytes: 10,
			CreatedAt: now, // 本轮开始之前就有
		}).Error; err != nil {
			t.Fatalf("seed prev asset: %v", err)
		}
	}

	items, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if len(items) != maxScreenshotsPerPR {
		t.Fatalf("items = %d, want %d（新增的那张应当落账并展示）", len(items), maxScreenshotsPerPR)
	}
	if !changed {
		t.Fatal("changed = false, want true（本轮新增的截图不能算进上一轮集合）")
	}
}

// prevRefs 的「本轮新行」截止是上一轮运行的完成时刻，不是本轮创建时刻
// （R12 审查意见）：探测/建运行跨同步时探测行落在本轮创建之前，按本轮创
// 建时刻剔除会把它们误算进上一轮，新增截图被判成「未变化」。
func TestAgentScreenshotsChangedCutoffAtPrevFinish(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![a](https://uiv.frp.musiver.cn/f/a.png)\n\n![b](https://uiv.frp.musiver.cn/f/b.png)"
	e.fake.SetPR(repo, 101, &githubapi.PullRequest{
		RepoFullName: repo, Number: 101, HeadSHA: "sha-same",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-cf")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	prevFinished := now.Add(-2 * time.Hour)
	prevRun := e.seedRun(t, repo, 101, "sha-same", model.ReviewRunCompleted, prevFinished)
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": prevFinished})
	curRun := e.seedRun(t, repo, 101, "sha-same", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	mk := func(id, url string, createdAt time.Time) *model.ReviewAsset {
		return &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 101, HeadSHA: "sha-same",
			Position: 0, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: createdAt,
		}
	}
	// a：上一轮完成之前就在（上一轮看到过，属上一轮集合）。
	if err := e.svc.db.Create(mk("asset-prev", "https://uiv.frp.musiver.cn/f/a.png", prevFinished.Add(-time.Hour))).Error; err != nil {
		t.Fatalf("seed prev asset: %v", err)
	}
	// b：上一轮完成之后、本轮创建之前落账（探测先行/建运行跨同步的特征），
	// 按旧口径会被误算进上一轮集合。
	if err := e.svc.db.Create(mk("asset-probe", "https://uiv.frp.musiver.cn/f/b.png", prevFinished.Add(time.Hour))).Error; err != nil {
		t.Fatalf("seed probe asset: %v", err)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true（新增截图 b 不得被算进上一轮集合）")
	}
}

// 轮次可重叠（manual rerun 恒开新轮、不看在途运行）：新轮创建可能早于上
// 一轮完成——prevCutoff 取两者更早者，新轮自己的早期行不得被算进上一轮集
// 合（R13 审查意见）。
func TestAgentScreenshotsChangedCutoffOverlappingRounds(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	// 本轮创建于 -45min；上一轮在 -30min 才完成（重叠 15 分钟）。
	nowUTC := time.Now().UTC()
	curCreatedAt := nowUTC.Add(-45 * time.Minute)
	prevFinished := nowUTC.Add(-30 * time.Minute)
	body := "![a](https://uiv.frp.musiver.cn/f/a.png)\n\n![b](https://uiv.frp.musiver.cn/f/b.png)"
	e.fake.SetPR(repo, 102, &githubapi.PullRequest{
		RepoFullName: repo, Number: 102, HeadSHA: "sha-same",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-ov")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	prevRun := e.seedRun(t, repo, 102, "sha-same", model.ReviewRunCompleted, curCreatedAt.Add(-2*time.Hour))
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": prevFinished})
	curRun := e.seedRun(t, repo, 102, "sha-same", model.ReviewRunQueued, curCreatedAt)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	mk := func(id, url string, createdAt time.Time) *model.ReviewAsset {
		return &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 102, HeadSHA: "sha-same",
			Position: 0, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: createdAt,
		}
	}
	// a：本轮创建之前就在（上一轮证据，计入 prevRefs）。
	if err := e.svc.db.Create(mk("asset-over-prev", "https://uiv.frp.musiver.cn/f/a.png", curCreatedAt.Add(-time.Hour))).Error; err != nil {
		t.Fatalf("seed prev asset: %v", err)
	}
	// b：落在本轮创建之后、上一轮完成之前的重叠窗口（新轮自己的早期证据，
	// 不得算进上一轮）。
	if err := e.svc.db.Create(mk("asset-over-new", "https://uiv.frp.musiver.cn/f/b.png", curCreatedAt.Add(15*time.Minute))).Error; err != nil {
		t.Fatalf("seed overlap asset: %v", err)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true（重叠窗口的新增行不得算进上一轮集合）")
	}
}

// 路径保留 + 文件缺失 + 重下失败（R12 修复的边界，R13 审查意见）：列表把
// 该行按带原因的错误项下发（不给必然打不开的 URL），审查员可据此记
// unmeasurable，而不是拿到 500 的图片地址。
func TestAgentScreenshotsMissingFileReportsReason(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![a](https://uiv.frp.musiver.cn/f/gone.png)"
	e.fake.SetPR(repo, 103, &githubapi.PullRequest{
		RepoFullName: repo, Number: 103, HeadSHA: "sha-gone",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-g")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})
	visualRun := e.seedRun(t, repo, 103, "sha-gone", model.ReviewRunQueued, now)
	e.svc.db.Model(visualRun).Update("kind", model.ReviewKindVisual)

	// 命中行：路径指向不存在的文件，重下必失败（503）——重下失败保留引用
	// 后行带非空路径 + error。
	e.media.anonErr = &githubmedia.UpstreamStatusError{StatusCode: 503}
	row := &model.ReviewAsset{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            "asset-gone", RepoFullName: repo, PRNumber: 103, HeadSHA: "sha-gone",
		Position: 0, Label: "a", SourceURL: "https://uiv.frp.musiver.cn/f/gone.png",
		StoragePath: "/cache/vanished-gone.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now,
	}
	if err := e.svc.db.Create(row).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	items, _, err := e.review.AgentScreenshots(context.Background(), visualRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Url != nil {
		t.Fatalf("url = %q, want 空（文件缺失不给 URL）", *items[0].Url)
	}
	if items[0].Error == nil || *items[0].Error == "" {
		t.Fatalf("error = %+v, want 带原因的错误项", items[0].Error)
	}
}

// 上一轮仍在跑（finished_at NULL）是重叠轮次的正常态：它没有可沿用的结
// 论，不得作为参照——本轮探测行（早于本轮创建、落在其运行窗口内）不算进
// 上一轮集合，参照回退到最近一个已完成的更早轮次（R14 审查意见）。
func TestAgentScreenshotsChangedIgnoresRunningPrevRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![a](https://uiv.frp.musiver.cn/f/a.png)\n\n![b](https://uiv.frp.musiver.cn/f/b.png)"
	e.fake.SetPR(repo, 104, &githubapi.PullRequest{
		RepoFullName: repo, Number: 104, HeadSHA: "sha-same",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-rn")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	// 轮次 1：已完成（finished_at = -3h），唯一有结论的参照。
	r1 := e.seedRun(t, repo, 104, "sha-same", model.ReviewRunCompleted, now.Add(-4*time.Hour))
	e.svc.db.Model(r1).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now.Add(-3 * time.Hour)})
	// 轮次 2：仍在跑（finished_at NULL），评论唤醒/manual rerun 的重叠常态。
	r2 := e.seedRun(t, repo, 104, "sha-same", model.ReviewRunRunning, now.Add(-2*time.Hour))
	e.svc.db.Model(r2).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})
	// 轮次 3：本轮。
	curRun := e.seedRun(t, repo, 104, "sha-same", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 3})

	mk := func(id, url string, createdAt time.Time) *model.ReviewAsset {
		return &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID, ID: id,
			RepoFullName: repo, PRNumber: 104, HeadSHA: "sha-same",
			Position: 0, Label: id, SourceURL: url,
			StoragePath: "/cache/" + id + ".png", MimeType: "image/png", SizeBytes: 10,
			CreatedAt: createdAt,
		}
	}
	// a：轮次 1 完成之前就在（参照集合）。
	if err := e.svc.db.Create(mk("asset-r1", "https://uiv.frp.musiver.cn/f/a.png", now.Add(-5*time.Hour))).Error; err != nil {
		t.Fatalf("seed r1 asset: %v", err)
	}
	// b：轮次 2 在途窗口内落账（探测先行，早于本轮创建）——不得算进参照。
	if err := e.svc.db.Create(mk("asset-r2probe", "https://uiv.frp.musiver.cn/f/b.png", now.Add(-time.Hour))).Error; err != nil {
		t.Fatalf("seed r2 asset: %v", err)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true（在途轮次的探测行不得算进参照集合）")
	}
}

// 上一轮结论不是 completed（准备阶段失败、从未把截图交给审查员）时不得作
// 参照：其资产行虽在，无人看过——本轮同集合不得判「未变化」（R15 审查意
// 见）。
func TestAgentScreenshotsChangedIgnoresFailedPrevRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![a](https://uiv.frp.musiver.cn/f/a.png)"
	e.fake.SetPR(repo, 105, &githubapi.PullRequest{
		RepoFullName: repo, Number: 105, HeadSHA: "sha-same",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-fl")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})

	// 上一轮：准备阶段失败（有完成时刻、结论不是 completed），但探测已把资
	// 产行留下。
	prevRun := e.seedRun(t, repo, 105, "sha-same", model.ReviewRunFailed, now.Add(-2*time.Hour))
	e.svc.db.Model(prevRun).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1, "finished_at": now.Add(-time.Hour)})
	curRun := e.seedRun(t, repo, 105, "sha-same", model.ReviewRunQueued, now)
	e.svc.db.Model(curRun).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 2})

	row := &model.ReviewAsset{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            "asset-failed", RepoFullName: repo, PRNumber: 105, HeadSHA: "sha-same",
		Position: 0, Label: "a", SourceURL: "https://uiv.frp.musiver.cn/f/a.png",
		StoragePath: "/cache/a.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now.Add(-90 * time.Minute),
	}
	if err := e.svc.db.Create(row).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	_, changed, err := e.review.AgentScreenshots(context.Background(), curRun.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true（失败轮次的资产集无人看过，不得作参照）")
	}
}

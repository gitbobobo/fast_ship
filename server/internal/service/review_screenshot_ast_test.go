package service

import (
	"context"
	"errors"
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
	probe := review.probeScreenshots(context.Background(), 1, body)
	if probe.result != probeNone {
		t.Fatalf("probe = %+v, want probeNone（全部不是图）", probe)
	}

	// 混合：徽章不是图 + png 拉不到 → probeUnavailable，原因取「拉不到」。
	e.media.anonErr = nil
	e.media.failURLs = map[string]error{
		badge: githubmedia.ErrUnsupportedUpstreamMedia,
		png:   errors.New("connection refused"),
	}
	probe = review.probeScreenshots(context.Background(), 1, body)
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
	probe = review.probeScreenshots(context.Background(), 1, body)
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
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 93, UserID: e.user.ID,
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
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 94, UserID: e.user.ID,
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
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 96, UserID: e.user.ID,
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
		ID: "asset-current", RepoFullName: repo, PRNumber: 95, HeadSHA: "sha-st",
		Position: 0, Label: "界面", SourceURL: "https://uiv.frp.musiver.cn/f/x.png",
		StoragePath: "/cache/x.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now,
	}
	stale := &model.ReviewAsset{
		ID: "asset-stale", RepoFullName: repo, PRNumber: 95, HeadSHA: "sha-st",
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

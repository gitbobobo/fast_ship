package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
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

// 视觉开轮前的截图探测：试完全部引用算内容指纹，任一成功即开轮；全部失败
// 不开轮并给中文 failure status。探测不写 review_assets，也不按资产行计数
// ——陈旧资产不能把「当前截图全失败」顶成可用。重试只发生在下一次同步事件
// （新推送/手动重跑）。

func seedVisualSyncEnv(t *testing.T, e *reviewEngineEnv, repo string, prNumber int, head string, now time.Time) {
	t.Helper()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, prNumber, &githubapi.PullRequest{
		RepoFullName: repo, Number: prNumber, HeadSHA: head,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-vu")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = combos
	})
	run := e.seedRun(t, repo, prNumber, head, model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"summary":     "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: prNumber, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: head, RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

// setLedgerPending 重新置 sync_pending，模拟下一次同步事件（手动重跑；
// reopened 等同 SHA 事件不会再次进入同步）。
func (e *reviewEngineEnv) setLedgerPending(t *testing.T, repo string, prNumber int) {
	t.Helper()
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			e.project.ID, repo, prNumber).
		Update("sync_pending", true).Error; err != nil {
		t.Fatalf("set sync_pending: %v", err)
	}
}

// 裸贴的外链图床链接（没有 ![]() / <img> 包裹）也要被收集——旧实现只收 GitHub
// 域的裸附件链接，PR 描述里直接粘贴 https://uiv…/f/x.png 这类裸外链收集不到
// → HasScreenshots=false → 静默不开视觉轮，连 failure status 都没有。判据是
// 「路径以位图扩展名结尾」，与下载面 imagesOnly 同一集合（不收 svg/视频）。
func TestCollectScreenshotURLsBareImageLinks(t *testing.T) {
	// 裸外链被收集，label 回退「截图 N」。
	refs := collectScreenshotURLs("验证截图 https://uiv.frp.musiver.cn/f/a.png 如上")
	if len(refs) != 1 {
		t.Fatalf("refs = %+v, want 1 条裸链接被收集", refs)
	}
	if refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("url = %q, want 裸链接原样收集", refs[0].URL)
	}
	if refs[0].Label != "截图 1" {
		t.Fatalf("label = %q, want 回退「截图 1」", refs[0].Label)
	}

	// GitHub 附件裸直链没有文件名扩展名（拖入/粘贴附件的标准形态），不要求
	// 扩展名——按「必须有位图扩展名」过滤会把它们漏掉 → HasScreenshots=false
	// → 静默不开视觉轮。
	refs = collectScreenshotURLs("附件直贴 https://github.com/user-attachments/assets/abc-123 如上")
	if len(refs) != 1 || refs[0].URL != "https://github.com/user-attachments/assets/abc-123" {
		t.Fatalf("refs = %+v, want 无扩展名的 GitHub 附件裸链被收集", refs)
	}
	refs = collectScreenshotURLs("历史图床 https://user-images.githubusercontent.com/123/456 如上")
	if len(refs) != 1 || refs[0].URL != "https://user-images.githubusercontent.com/123/456" {
		t.Fatalf("refs = %+v, want 无扩展名的 user-images 裸链被收集", refs)
	}

	// # 与紧贴的中文句读不是资源的一部分：fragment 会被下载面 normalize 拒
	// （IsGitHubMediaURL 也因此判 false，bearer 重试失效），句读会破坏扩展名
	// 判定——收集时都要剥掉。
	refs = collectScreenshotURLs("见 https://uiv.frp.musiver.cn/f/a.png#frag")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("refs = %+v, want 收集的 URL 不含 #frag", refs)
	}
	refs = collectScreenshotURLs("见 https://uiv.frp.musiver.cn/f/a.png。")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("refs = %+v, want 紧跟中文句号的链接被收集且不含「。」", refs)
	}

	// 半角句尾标点（. , : ! ?）与全角句读（；：！？…）、右侧引号粘在裸链
	// 尾部不算资源本身：位图扩展名带尾标点判 false → 漏收 → HasScreenshots=
	// false → 视觉轮静默不开；GitHub 附件形态带尾标点发请求 404 → 误写「截
	// 图无法下载」。GFM/Linkify 剥它管的集合，其余由兜底裁剪剥——都只裁尾
	// 部，中段/查询串里的合法标点不动。ASCII `;` 不在内：Linkify 保留它（只
	// 把结尾的 &xxx; 实体当整体剥），与 GitHub 渲染出的链接一致，`.png;` 这
	// 种粘了分号的裸链不再收集。
	for _, p := range []string{".", ",", ":", "!", "?", "；", "：", "！", "？", "…", "”"} {
		refs = collectScreenshotURLs("see https://uiv.frp.musiver.cn/f/a.png" + p)
		if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
			t.Fatalf("refs = %+v (尾标点 %q), want 裁掉尾部标点后收集", refs, p)
		}
	}
	// 只裁尾部不动中段：行中逗号后还有正文时，剥掉的也只是匹配尾部的逗号。
	refs = collectScreenshotURLs("see https://uiv.frp.musiver.cn/f/a.png, and more")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("refs = %+v, want 行中逗号同样剥掉", refs)
	}
	// 带查询串的正常链接原样收集：结尾不是标点，裁剪不动查询串。
	refs = collectScreenshotURLs("see https://uiv.frp.musiver.cn/f/a.png?raw=1")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png?raw=1" {
		t.Fatalf("refs = %+v, want 查询串原样保留", refs)
	}
	// GitHub 附件形态的裸链同样剥尾标点：附件无扩展名也能收集，粘上 `.` 发
	// 请求 404 会误写「截图无法下载」。
	refs = collectScreenshotURLs("see https://github.com/user-attachments/assets/abc.")
	if len(refs) != 1 || refs[0].URL != "https://github.com/user-attachments/assets/abc" {
		t.Fatalf("refs = %+v, want 附件裸链收集且不含尾部 `.`", refs)
	}

	// 无位图扩展名的普通网页链接不收（github.com 的非附件路径同样不收——
	// 豁免只给 user-attachments/assets）。
	if refs := collectScreenshotURLs("看这个页面 https://example.com/page 谢谢"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want 非位图链接不收集", refs)
	}
	if refs := collectScreenshotURLs("打开 https://github.com/owner/repo/pull/1 看看"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want github.com 非附件路径不收集", refs)
	}
	// svg / 视频扩展名不收（视觉审查只吃位图）。
	if refs := collectScreenshotURLs("https://example.com/a.svg https://example.com/b.mp4"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want svg/视频不收集", refs)
	}

	// Markdown + 裸链 + HTML 混排：按原文位置排序，同 URL 只取首次（带 label
	// 的 Markdown 引用优先于裸链）。
	body := "![首图](https://uiv.frp.musiver.cn/f/one.png) 然后裸贴 https://uiv.frp.musiver.cn/f/two.jpg " +
		"再贴一次同图 https://uiv.frp.musiver.cn/f/one.png 末尾 <img src=\"https://uiv.frp.musiver.cn/f/three.png\">"
	refs = collectScreenshotURLs(body)
	wantURLs := []string{
		"https://uiv.frp.musiver.cn/f/one.png",
		"https://uiv.frp.musiver.cn/f/two.jpg",
		"https://uiv.frp.musiver.cn/f/three.png",
	}
	if len(refs) != len(wantURLs) {
		t.Fatalf("refs = %+v, want %d 条（去重后）", refs, len(wantURLs))
	}
	for i, want := range wantURLs {
		if refs[i].URL != want {
			t.Fatalf("refs[%d].url = %q, want %q（按原文位置排序）", i, refs[i].URL, want)
		}
	}
	if refs[0].Label != "首图" {
		t.Fatalf("refs[0].label = %q, want Markdown alt 文本优先", refs[0].Label)
	}
	if refs[1].Label != "截图 2" || refs[2].Label != "截图 3" {
		t.Fatalf("labels = %q/%q, want 回退「截图 2/3」", refs[1].Label, refs[2].Label)
	}
}

// J1 回归：PR 描述只有 svg 徽章这类「不是图」的引用时，探测逐张被下载面以内
// 容拒绝（ErrUnsupportedUpstreamMedia）→ 三态收敛 probeNone → HasScreenshots=
// false → visualNone——不建视觉运行，也不写「截图无法下载」failure（visual_
// status 保持空）。旧实现收集为空/全失败两态不分，要么漏收要么每个同步事件
// 刷一条 failure，永远收敛不到「没有截图」。
func TestVisualSyncBadgeOnlySkipsScreenshotFlow(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 92, "sha-b", now)
	badgeBody := "![badge](https://img.shields.io/badge/build-passing.svg) " +
		"<img src=\"https://img.shields.io/badge/coverage-90%25.svg\">"
	e.fake.SetPR(repo, 92, &githubapi.PullRequest{
		RepoFullName: repo, Number: 92, HeadSHA: "sha-b",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		Body: badgeBody,
	})

	media := e.media
	// 显式声明的徽章引用照常收集（准入不再按扩展名拒绝），由探测的
	// Content-Type 裁决：下载面拒 svg → 「不是图」→ probeNone。
	media.failURLs = map[string]error{
		"https://img.shields.io/badge/build-passing.svg":  githubmedia.ErrUnsupportedUpstreamMedia,
		"https://img.shields.io/badge/coverage-90%25.svg": githubmedia.ErrUnsupportedUpstreamMedia,
	}
	e.review.syncPendingPRs(now)

	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).Where("kind = ?", model.ReviewKindVisual).Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（全部不是图不开视觉轮）", visual)
	}
	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 0 {
		t.Fatalf("statuses = %+v, want 0（全部不是图走 visualNone，不写「截图无法下载」failure）", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 92", repo)
	if ledger.VisualStatus != "" {
		t.Fatalf("visual_status = %q, want 空（全部不是图 ≠ 截图无法下载）", ledger.VisualStatus)
	}

	// 描述换成真实可下载的截图后，下一次同步事件照常开轮——准入没有把该收
	// 的也挡掉。
	e.fake.SetPR(repo, 92, &githubapi.PullRequest{
		RepoFullName: repo, Number: 92, HeadSHA: "sha-b",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		Body: "![界面](https://uiv.frp.musiver.cn/f/x.png)",
	})
	media.failURLs = nil
	e.setLedgerPending(t, repo, 92)
	e.review.syncPendingPRs(now.Add(time.Second))
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ?", repo, 92, model.ReviewKindVisual).Find(&visualRuns)
	if len(visualRuns) == 0 {
		t.Fatal("描述换成真实截图后应开视觉轮")
	}
}

// J1：结构准入严格镜像下载面 normalize 的拒绝集（https + host 非空 + 无
// userinfo），不按扩展名拒绝——显式声明的图（![]()/<img>）交给探测的
// Content-Type 裁决：render.php?id=1 这类无扩展名直链可能是真图，扩展名误
// 判会漏收（视觉轮静默不开）；「全部不是图」由探测三态收敛成 visualNone。
func TestCollectScreenshotURLsAdmitsDownloadableOnly(t *testing.T) {
	// 下载面必然拒绝的形态不收：非 https、相对路径、userinfo。
	for _, body := range []string{
		"![图](http://uiv.frp.musiver.cn/f/x.png)",
		"<img src=\"http://uiv.frp.musiver.cn/f/x.png\">",
		"![内联](data:image/png;base64,iVBORw0KGgo=)",
		"<img src=\"data:image/png;base64,iVBORw0KGgo=\">",
		"![相对](images/shot.png)",
		"<img src=\"images/shot.png\">",
		"![凭证](https://user:pass@uiv.frp.musiver.cn/f/x.png)",
		"<img src=\"https://user:pass@uiv.frp.musiver.cn/f/x.png\">",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 0 {
			t.Fatalf("body %q: refs = %+v, want 空（下载面必然拒绝的形态不收）", body, refs)
		}
	}

	// 无扩展名/怪扩展名的 https 图片（可能是 image/* 直链）、GitHub 附件形态
	// （附件直链本就没有扩展名）与 svg/html/mp4 网页形态都被收集——是不是图
	// 由探测裁决。
	for _, body := range []string{
		"![图](https://images.example.com/abc123)",
		"<img src=\"https://images.example.com/abc123\">",
		"![附件](https://github.com/user-attachments/assets/uuid-1)",
		"<img src=\"https://user-images.githubusercontent.com/1/2\">",
		"![徽章](https://img.shields.io/badge/x.svg)",
		"<img src=\"https://img.shields.io/badge/x.svg\">",
		"![动态](https://example.com/render.php?id=1)",
		"![网页](https://example.com/docs/page.html)",
		"![视频](https://example.com/demo.mp4)",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 1 {
			t.Fatalf("body %q: refs = %+v, want 显式声明的 https 图片被收集（探测裁决）", body, refs)
		}
	}

	// 裸链的额外准入不变：必须 GitHub 附件形态 ∪ 位图扩展名，svg/网页/无扩
	// 展名的裸网页链接仍不收。
	for _, body := range []string{
		"徽章 https://img.shields.io/badge/x.svg 谢谢",
		"页面 https://example.com/docs/page.html 谢谢",
		"直链 https://images.example.com/abc123 谢谢",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 0 {
			t.Fatalf("body %q: refs = %+v, want 裸链无附件形态/位图扩展名不收", body, refs)
		}
	}
}

// J2：包法符号的处理。AST 收集后行内代码不再产链接节点（反引号包住的 URL
// 是代码，不是截图引用——旧正则会误收）；中文直角引号紧贴 URL 时不触发
// Linkify 的自动链接（GitHub 渲染同款行为：URL 前面必须是空白/强调符/括号，
// 「https://…」在 GitHub 上就是纯文本，旧正则会误收）；强调符包住的裸链
// 照常收集，尾部粘住的包法符号由 GFM 剥掉。
func TestCollectScreenshotURLsTrimsWrappingPunct(t *testing.T) {
	// 行内代码里的 URL 不是截图引用：不产节点，不收集。
	if refs := collectScreenshotURLs("看 `https://uiv.frp.musiver.cn/f/a.png` 这段"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want 行内代码里的 URL 不收集", refs)
	}
	// 围栏代码块同样不收集。
	if refs := collectScreenshotURLs("```\n![a](https://uiv.frp.musiver.cn/f/a.png)\n```"); len(refs) != 0 {
		t.Fatalf("refs = %+v, want 围栏代码块里的图片语法不收集", refs)
	}
	// 紧贴直角引号的 URL 在 GitHub 上不渲染成链接，照 GitHub 的口径不收集。
	for _, body := range []string{
		"见「https://uiv.frp.musiver.cn/f/a.png」 这段",
		"书名『https://uiv.frp.musiver.cn/f/a.png』 完",
	} {
		if refs := collectScreenshotURLs(body); len(refs) != 0 {
			t.Fatalf("body %q: refs = %+v, want 紧贴引号不触发自动链接（GitHub 同款）", body, refs)
		}
	}
	// 空白分隔 + 尾部包法符号照常收集。
	refs := collectScreenshotURLs("重点 *https://uiv.frp.musiver.cn/f/a.png* 收尾")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("refs = %+v, want 强调符包住的裸链剥尾收集", refs)
	}
}

// J3：md/HTML 引用里的 #fragment 统一剥掉——fragment 不是资源本身，下载面
// normalize 会拒、IsGitHubMediaURL 也因此判 false，私有附件带 fragment 时
// 不会触发 bearer 重试；收集结果与后续指纹都用去 fragment 后的 URL。
func TestCollectScreenshotURLsStripsFragmentFromMarkup(t *testing.T) {
	refs := collectScreenshotURLs("![附件](https://github.com/user-attachments/assets/uuid#frag)")
	if len(refs) != 1 || refs[0].URL != "https://github.com/user-attachments/assets/uuid" {
		t.Fatalf("refs = %+v, want md 引用收集的 URL 不含 #frag", refs)
	}
	refs = collectScreenshotURLs("<img src=\"https://uiv.frp.musiver.cn/f/a.png#y\">")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/a.png" {
		t.Fatalf("refs = %+v, want <img> 引用收集的 URL 不含 #y", refs)
	}
	// fragment 之后粘的句读同样剥干净（md 捕获会一路吃到 `)` 前）。
	refs = collectScreenshotURLs("![附件](https://github.com/user-attachments/assets/uuid#frag。)")
	if len(refs) != 1 || refs[0].URL != "https://github.com/user-attachments/assets/uuid" {
		t.Fatalf("refs = %+v, want fragment 与尾标点都剥掉", refs)
	}
	// 带/不带 fragment 的同 URL 规整后视为同一张：去重按规整后的 URL，只留
	// 首次出现（label 取先到的 alt）。
	refs = collectScreenshotURLs("![a](https://uiv.frp.musiver.cn/f/one.png#f1) ![b](https://uiv.frp.musiver.cn/f/one.png)")
	if len(refs) != 1 || refs[0].URL != "https://uiv.frp.musiver.cn/f/one.png" || refs[0].Label != "a" {
		t.Fatalf("refs = %+v, want 规整后去重只留一条且 label 取先到的 alt", refs)
	}
}

func TestVisualSyncScreensUnavailable(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 83, "sha-u", now)

	media := e.media
	media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	// 不建视觉运行，探测也不落账（资产行由视觉运行时的 ensureAssets 负责）。
	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).Where("kind = ?", model.ReviewKindVisual).Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（截图全部下载失败不开轮）", visual)
	}
	var assets int64
	e.svc.db.Model(&model.ReviewAsset{}).Where("repo_full_name = ? AND pr_number = 83", repo).Count(&assets)
	if assets != 0 {
		t.Fatalf("review assets = %d, want 0（探测不写资产行）", assets)
	}
	// 账本：visual_status=failure、指纹不入账（下次同步事件指纹仍不同，会重试）。
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 83", repo)
	if ledger.VisualStatus != "failure" || ledger.VisualScreensFingerprint != "" {
		t.Fatalf("ledger = %q/%q, want failure/空指纹", ledger.VisualStatus, ledger.VisualScreensFingerprint)
	}
	// status 是中文原因，网络层英文原文不得透出。
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "failure" {
		t.Fatalf("statuses = %+v, want 一条 failure", statuses)
	}
	desc := statuses[0].Description
	if !strings.Contains(desc, "截图无法下载") || strings.Contains(desc, "connection refused") || strings.Contains(desc, "dial tcp") {
		t.Fatalf("status desc = %q, want 中文原因且不透出英文网络错误", desc)
	}

	// 下一次同步事件（模拟手动重跑）：每次都写当前原因，不再沿用旧描述。
	e.setLedgerPending(t, repo, 83)
	e.review.syncPendingPRs(now.Add(time.Second))
	if got := e.fake.StatusesForContext(reviewVisualStatusContext); len(got) != 2 {
		t.Fatalf("statuses = %d, want 2（每次同步事件都写当前原因）", len(got))
	}

	// media 恢复后的下一次同步事件：开视觉轮并写入指纹。
	media.anonErr = nil
	e.setLedgerPending(t, repo, 83)
	e.review.syncPendingPRs(now.Add(2 * time.Second))
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ?", repo, 83, model.ReviewKindVisual).Find(&visualRuns)
	if len(visualRuns) == 0 {
		t.Fatal("media 恢复后应开视觉轮")
	}
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 83", repo)
	if ledger.VisualStatus != "pending" || ledger.VisualScreensFingerprint == "" {
		t.Fatalf("ledger = %q/%q, want pending/指纹", ledger.VisualStatus, ledger.VisualScreensFingerprint)
	}
}

// 探测不再「第一张成功即停」：每个成功图都要参与内容指纹（「URL 换了但图
// 没变」判定的依据），三张里第一张可用也把其余的试完；失败的算「拉不到」
// 不算内容，照常开轮。部分失败时内容指纹未知——但 URL 集合维度照常推进
// （本轮开轮的材料是什么要记下来），后续同一集合不再重探。
func TestVisualSyncProbeTriesAllRefsForContentFingerprint(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 84, "sha-p", now)

	body := "![a](https://uiv.frp.musiver.cn/f/ok.png) ![b](https://uiv.frp.musiver.cn/f/bad1.png) ![c](https://uiv.frp.musiver.cn/f/bad2.png)"
	e.fake.SetPR(repo, 84, &githubapi.PullRequest{
		RepoFullName: repo, Number: 84, HeadSHA: "sha-p",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		Body: body,
	})
	e.media.failURLs = map[string]error{
		"https://uiv.frp.musiver.cn/f/bad1.png": errors.New("404 not found"),
		"https://uiv.frp.musiver.cn/f/bad2.png": errors.New("404 not found"),
	}
	e.review.syncPendingPRs(now)

	if e.media.anonCalls != 3 {
		t.Fatalf("anonCalls = %d, want 3（每张都要试，为内容指纹）", e.media.anonCalls)
	}
	var visualRuns []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = ? AND kind = ?", repo, 84, model.ReviewKindVisual).Find(&visualRuns)
	if len(visualRuns) == 0 {
		t.Fatal("第一张可用时应照常开视觉轮")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 84", repo)
	if ledger.VisualStatus != "pending" {
		t.Fatalf("visual_status = %q, want pending", ledger.VisualStatus)
	}
	// 只成功一部分：内容维度未知不入账，但 URL 集合维度要推进——账本描述的
	// 是本轮开轮的材料，不再留着旧集合的指纹被后续轮次误命中。
	stored := parseScreensFingerprint(ledger.VisualScreensFingerprint)
	wantURLFP := screenshotFingerprint(collectScreenshotURLs(body))
	if stored.urlFP != wantURLFP || stored.contentFP != "" || stored.settledNone {
		t.Fatalf("fingerprint = %+v (%q), want urlFP=%s 且内容维度未知（裸 urlFP 形态）",
			stored, ledger.VisualScreensFingerprint, wantURLFP)
	}
	// 同一 body 再来一次同步事件：视觉运行在途走汇总路径，不再进决策——
	// 不重探、不重开轮。
	e.setLedgerPending(t, repo, 84)
	e.review.syncPendingPRs(now.Add(time.Second))
	if e.media.anonCalls != 3 {
		t.Fatalf("anonCalls = %d, want 仍为 3（运行在途不重探）", e.media.anonCalls)
	}
}

// 陈旧资产不参与可用性判断：描述已移除旧 URL、只剩一个下载失败的 URL 时，
// 不能因旧资产行 storage_path 非空就误判「有可用截图」。
func TestVisualSyncIgnoresStaleAssets(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 85, "sha-s", now)

	// 上一轮留下的陈旧资产行：URL 已不在当前描述里，storage_path 非空。
	stale := &model.ReviewAsset{
		ID: "stale-asset", RepoFullName: repo, PRNumber: 85, HeadSHA: "sha-s",
		Position: 0, Label: "旧图", SourceURL: "https://uiv.frp.musiver.cn/f/old.png",
		StoragePath: "/cache/old.png", MimeType: "image/png", SizeBytes: 10,
		CreatedAt: now,
	}
	if err := e.svc.db.Create(stale).Error; err != nil {
		t.Fatalf("seed stale asset: %v", err)
	}
	// 当前描述只剩一张下载失败的图。
	e.media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	var visual int64
	e.svc.db.Model(&model.ReviewRun{}).Where("kind = ?", model.ReviewKindVisual).Count(&visual)
	if visual != 0 {
		t.Fatalf("visual runs = %d, want 0（陈旧资产不算可用截图）", visual)
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "failure" || !strings.Contains(statuses[0].Description, "截图无法下载") {
		t.Fatalf("statuses = %+v, want 一条「截图无法下载」failure", statuses)
	}
}

// F20 回归：截图指纹未变 + 上一轮视觉含 blocker + 下载全失败 → 沿用上一轮
// failure 带过，不得误写成「截图无法下载」。旧实现在这里照样探测并失败，
// 把上一轮的带过分支连同「截图复核意见尚未处理」评论一起吞掉。
func TestVisualSyncProbeSkippedCarriesPriorFailure(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 86, &githubapi.PullRequest{
		RepoFullName: repo, Number: 86, HeadSHA: "sha-cf", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-vf")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉发现 blocker。
	prior := e.seedRun(t, repo, 86, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断问题","findings":[{"severity":"blocker","screenshot":"界面","dimension":"hierarchy","body":"按钮叠在文字上","fix":"调整层级"}],"prior":[],"unmeasurable":[]}`,
	})
	// 本轮（round 2）代码通过、UI 有改动，但截图集合没变、上轮视觉后无 UI 改动
	// ——本来就不会开新轮，不该触发探测。
	run := e.seedRun(t, repo, 86, "sha-cf", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 86, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cf", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	// 图床挂了：指纹未变不该探测，探测失败也不能改写带过路径的结论。
	e.media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（指纹未变不探测）", e.media.anonCalls)
	}
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "failure" {
		t.Fatalf("statuses = %+v, want 一条 failure（沿用上一轮结论）", statuses)
	}
	desc := statuses[0].Description
	if !strings.Contains(desc, "截图复核意见尚未处理") || strings.Contains(desc, "截图无法下载") {
		t.Fatalf("status desc = %q, want 沿用上一轮 failure 的带过文案", desc)
	}
	// 带过评论仍要产生（旧实现会把它一起吞掉）。
	found := false
	for _, c := range e.fake.Comments(repo, 86) {
		if strings.Contains(c, reviewVisualCommentMarker) && strings.Contains(c, "沿用上一轮视觉审查结论") {
			found = true
		}
	}
	if !found {
		t.Fatalf("comments = %v, want 视觉带过评论", e.fake.Comments(repo, 86))
	}
	// 带过路径的代码顶层评论同样带「截图复核意见尚未处理」（visualCarryFailure
	// 分支的既有行为，与截图不可用分支的口径对齐）。
	hint := false
	for _, c := range e.fake.Comments(repo, 86) {
		if strings.Contains(c, reviewCodeCommentMarker) && strings.Contains(c, "截图复核意见尚未处理") {
			hint = true
		}
	}
	if !hint {
		t.Fatalf("code comments = %v, want 含「截图复核意见尚未处理」", e.fake.Comments(repo, 86))
	}
}

// F20 回归：截图指纹未变 + 上一轮视觉通过 + 下载失败 → 沿用 success，不因
// 下载失败变红。
func TestVisualSyncProbeSkippedKeepsPriorSuccess(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 87, &githubapi.PullRequest{
		RepoFullName: repo, Number: 87, HeadSHA: "sha-cs", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-vp")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉通过。
	prior := e.seedRun(t, repo, 87, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 87, "sha-cs", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 87, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cs", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	if e.media.anonCalls != 0 {
		t.Fatalf("anonCalls = %d, want 0（指纹未变不探测）", e.media.anonCalls)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 87", repo)
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "success" {
		t.Fatalf("statuses = %+v, want 一条 success（沿用上一轮结论，不因下载失败变红）", statuses)
	}
	if !strings.Contains(statuses[0].Description, "沿用上一轮") {
		t.Fatalf("status desc = %q, want 沿用文案", statuses[0].Description)
	}
	if ledger.VisualStatus != "success" {
		t.Fatalf("visual_status = %q, want success", ledger.VisualStatus)
	}
	// 沿用 success 时上一轮没有遗留：代码顶层评论不得带「截图复核意见尚未
	// 处理」（拿不存在的遗留去催复核者会误导）。
	for _, c := range e.fake.Comments(repo, 87) {
		if strings.Contains(c, reviewCodeCommentMarker) && strings.Contains(c, "截图复核意见尚未处理") {
			t.Fatalf("code comment = %q, 沿用 success 不应带遗留提示", c)
		}
	}
}

// F26 回归：上一轮视觉有 blocker → 早先同步走 carry 分支，把「沿用上一轮
// 视觉审查结论（截图未变化，意见尚未处理）」发到 PR 上并记 visual_comment_id；
// 随后 PR 描述换了截图集合（指纹变化）、新截图全部下载失败 → 本轮决策变为
// 「截图无法下载」。此时已存在的视觉评论必须改写成「截图全部无法下载」并
// 保留上一轮意见条目——「截图未变化」的旧说法与当前 head 不符。
func TestVisualSyncScreensUnavailableUpdatesExistingVisualComment(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 88, &githubapi.PullRequest{
		RepoFullName: repo, Number: 88, HeadSHA: "sha-cu", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-cu")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉发现 blocker。
	prior := e.seedRun(t, repo, 88, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断问题","findings":[{"severity":"blocker","screenshot":"界面","dimension":"hierarchy","body":"按钮叠在文字上","fix":"调整层级"}],"prior":[],"unmeasurable":[]}`,
	})
	// 本轮（round 2）：截图指纹未变、上轮视觉后无 UI 改动 → 第一次同步走
	// carry 分支发出「沿用上一轮」评论，账本记 visual_comment_id。
	run := e.seedRun(t, repo, 88, "sha-cu", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 88, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cu", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 88", repo)
	if ledger.VisualCommentID == 0 {
		t.Fatal("carry 同步后账本应有 visual_comment_id（场景前置不成立）")
	}

	// 第二次同步事件：PR 描述换成新截图集合（指纹变化、head 不变），新截图
	// 全部下载失败。
	e.fake.SetPR(repo, 88, &githubapi.PullRequest{
		RepoFullName: repo, Number: 88, HeadSHA: "sha-cu",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		Body: "![新界面](https://uiv.frp.musiver.cn/f/new.png)",
	})
	e.media.anonErr = errors.New("connection refused")
	e.setLedgerPending(t, repo, 88)
	e.review.syncPendingPRs(now.Add(time.Second))

	// 已存在的视觉评论被改写：仍只有一条（更新而非新发），不再有「截图未
	// 变化」，换成「截图全部无法下载」语义并保留上一轮 finding 文本。
	var visuals []string
	for _, c := range e.fake.Comments(repo, 88) {
		if strings.Contains(c, reviewVisualCommentMarker) {
			visuals = append(visuals, c)
		}
	}
	if len(visuals) != 1 {
		t.Fatalf("visual comments = %d, want 1（更新既有评论而不是新发）", len(visuals))
	}
	comment := visuals[0]
	if strings.Contains(comment, "截图未变化") {
		t.Fatalf("comment = %q, 不应再宣称「截图未变化」", comment)
	}
	if !strings.Contains(comment, "全部无法下载") || !strings.Contains(comment, "未做视觉审查") {
		t.Fatalf("comment = %q, want 「截图全部无法下载，未做视觉审查」语义", comment)
	}
	if !strings.Contains(comment, "按钮叠在文字上") {
		t.Fatalf("comment = %q, want 保留上一轮 finding 文本", comment)
	}
}

// G4 回归：upsertPRComment 末尾写回 visual_comment_id 失败（账本 id 被留成
// 0，PR 上评论其实已发出）时，「截图无法下载」分支旧实现按
// VisualCommentID != 0 短路，跳过改写——上一轮 carry 发的「截图未变化」评论
// 原样留在 PR 上，与「截图无法下载」的 failure status 互相矛盾。新实现只看
// 上一轮有无视觉运行，savedID 为 0 时按 marker 找回既有评论改写。
func TestVisualSyncScreensUnavailableRewritesCarryCommentWithoutLedgerID(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 91, &githubapi.PullRequest{
		RepoFullName: repo, Number: 91, HeadSHA: "sha-lr", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-lr")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉发现 blocker。
	prior := e.seedRun(t, repo, 91, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断问题","findings":[{"severity":"blocker","screenshot":"界面","dimension":"hierarchy","body":"按钮叠在文字上","fix":"调整层级"}],"prior":[],"unmeasurable":[]}`,
	})
	// 本轮（round 2）：截图指纹未变 → 第一次同步走 carry 分支发出「沿用上一
	// 轮」评论，账本记 visual_comment_id。
	run := e.seedRun(t, repo, 91, "sha-lr", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 91, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-lr", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.review.syncPendingPRs(now)

	// carry 评论已发出，但模拟写回失败：账本 visual_comment_id 被留成 0。
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 91", repo)
	if ledger.VisualCommentID == 0 {
		t.Fatal("carry 同步后账本应有 visual_comment_id（场景前置不成立）")
	}
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			e.project.ID, repo, 91).
		Update("visual_comment_id", 0).Error; err != nil {
		t.Fatalf("清零 visual_comment_id: %v", err)
	}

	// 第二次同步事件：PR 描述换成新截图集合（指纹变化、head 不变），新截图
	// 全部下载失败。
	e.fake.SetPR(repo, 91, &githubapi.PullRequest{
		RepoFullName: repo, Number: 91, HeadSHA: "sha-lr",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		Body: "![新界面](https://uiv.frp.musiver.cn/f/new.png)",
	})
	e.media.anonErr = errors.New("connection refused")
	e.setLedgerPending(t, repo, 91)
	e.review.syncPendingPRs(now.Add(time.Second))

	// PR 上那条 carry 评论被改写：仍只有一条（按 marker 找回更新而非新发），
	// 不再有「截图未变化」，换成「截图全部无法下载」语义并保留上一轮 finding。
	var visuals []string
	for _, c := range e.fake.Comments(repo, 91) {
		if strings.Contains(c, reviewVisualCommentMarker) {
			visuals = append(visuals, c)
		}
	}
	if len(visuals) != 1 {
		t.Fatalf("visual comments = %d, want 1（按 marker 找回既有评论更新）", len(visuals))
	}
	comment := visuals[0]
	if strings.Contains(comment, "截图未变化") {
		t.Fatalf("comment = %q, 不应再宣称「截图未变化」", comment)
	}
	if !strings.Contains(comment, "全部无法下载") || !strings.Contains(comment, "未做视觉审查") {
		t.Fatalf("comment = %q, want 「截图全部无法下载，未做视觉审查」语义", comment)
	}
	if !strings.Contains(comment, "按钮叠在文字上") {
		t.Fatalf("comment = %q, want 保留上一轮 finding 文本", comment)
	}
}

// F26 补充：账本没有已存在的视觉评论（visual_comment_id = 0）时，「截图无法
// 下载」分支不新发评论——结论已由 failure status 表达，避免每个失败的 head
// 都多一条只有说明的评论。
func TestVisualSyncScreensUnavailableSkipsCommentWithoutPrior(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	seedVisualSyncEnv(t, e, repo, 89, "sha-nc", now)

	e.media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 89", repo)
	if ledger.VisualStatus != "failure" {
		t.Fatalf("visual_status = %q, want failure（场景前置不成立）", ledger.VisualStatus)
	}
	for _, c := range e.fake.Comments(repo, 89) {
		if strings.Contains(c, reviewVisualCommentMarker) {
			t.Fatalf("comment = %q, want 不新发视觉评论", c)
		}
	}
}

// E5 回归：上一轮视觉含 blocker + 本轮截图集合已换且全部下载失败 → 决策是
// 「截图无法下载」（visualScreensUnavailable），但代码顶层评论仍要带「截图
// 复核意见尚未处理」——与 visualCarryFailure 分支口径一致。旧实现在这个分支
// 恒置 visualCarry=false，提示丢失，复核者不知道上一轮意见还挂着。
func TestVisualSyncScreensUnavailableCarriesPriorHint(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 90, &githubapi.PullRequest{
		RepoFullName: repo, Number: 90, HeadSHA: "sha-sh", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-sh")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉发现 blocker。
	prior := e.seedRun(t, repo, 90, "sha-prev", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断问题","findings":[{"severity":"blocker","screenshot":"界面","dimension":"hierarchy","body":"按钮叠在文字上","fix":"调整层级"}],"prior":[],"unmeasurable":[]}`,
	})
	// 本轮（round 2）代码通过；账本未记截图指纹 → ScreensChanged=true，
	// 探测真的发生。
	run := e.seedRun(t, repo, 90, "sha-sh", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":true}`,
	})
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 90, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-sh", RoundNumber: 2,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	e.media.anonErr = errors.New("connection refused")
	e.review.syncPendingPRs(now)

	// 决策确实是「截图无法下载」分支。
	statuses := e.fake.StatusesForContext(reviewVisualStatusContext)
	if len(statuses) != 1 || statuses[0].State != "failure" || !strings.Contains(statuses[0].Description, "截图无法下载") {
		t.Fatalf("statuses = %+v, want 一条「截图无法下载」failure", statuses)
	}
	// 代码顶层评论带遗留提示。
	hint := false
	for _, c := range e.fake.Comments(repo, 90) {
		if strings.Contains(c, reviewCodeCommentMarker) && strings.Contains(c, "截图复核意见尚未处理") {
			hint = true
		}
	}
	if !hint {
		t.Fatalf("code comments = %v, want 含「截图复核意见尚未处理」", e.fake.Comments(repo, 90))
	}
}

// F32 回归：单张探测时限必须短于总预算。第一个 URL 黑洞（阻塞到 ctx.Done），
// 第二个可用——旧实现没有单张时限，第一张吃满总预算后探测放弃，第二张永远
// 轮不到，决策落入「截图无法下载」且每次同步重复同一张挂死图；新实现单张
// 超时后用剩余预算继续试下一张。
func TestProbeScreenshotsAttemptTimeoutLetsLaterScreensThrough(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	hangURL := "https://uiv.frp.musiver.cn/f/hang.png"
	okURL := "https://uiv.frp.musiver.cn/f/ok.png"
	e.media.blockURLs = map[string]bool{hangURL: true}

	origAttempt, origTotal := screenshotProbeAttemptTimeout, screenshotProbeTimeout
	screenshotProbeAttemptTimeout = 100 * time.Millisecond
	screenshotProbeTimeout = 1 * time.Second
	defer func() {
		screenshotProbeAttemptTimeout, screenshotProbeTimeout = origAttempt, origTotal
	}()

	body := "![a](" + hangURL + ") ![b](" + okURL + ")"
	start := time.Now()
	probe := review.probeScreenshots(context.Background(), 1, body)
	elapsed := time.Since(start)

	if probe.result != probeReady {
		t.Fatalf("probe = %+v, want probeReady——第一张挂死不应饿死后面的可用图", probe)
	}
	if e.media.anonCalls != 2 {
		t.Fatalf("anonCalls = %d, want 2（第二张必须被尝试）", e.media.anonCalls)
	}
	if probe.contentFP != "" {
		t.Fatalf("contentFP = %q, want 空（部分失败内容未知）", probe.contentFP)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("elapsed = %v, want 远小于总预算", elapsed)
	}
}

// F32：所有 URL 都黑洞时，探测必须在总预算附近返回失败与非空原因，不能
// 无限等待。
func TestProbeScreenshotsAllBlockedStopsAtBudget(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	e.media.blockURLs = map[string]bool{}
	var refs []string
	for i := 0; i < 15; i++ {
		u := fmt.Sprintf("https://uiv.frp.musiver.cn/f/hang%d.png", i)
		e.media.blockURLs[u] = true
		refs = append(refs, fmt.Sprintf("![s%d](%s)", i, u))
	}

	origAttempt, origTotal := screenshotProbeAttemptTimeout, screenshotProbeTimeout
	screenshotProbeAttemptTimeout = 100 * time.Millisecond
	screenshotProbeTimeout = 1 * time.Second
	defer func() {
		screenshotProbeAttemptTimeout, screenshotProbeTimeout = origAttempt, origTotal
	}()

	start := time.Now()
	probe := review.probeScreenshots(context.Background(), 1, strings.Join(refs, " "))
	elapsed := time.Since(start)

	if probe.result != probeUnavailable {
		t.Fatalf("probe = %+v, want probeUnavailable（全部挂死，预算耗尽未探完也算拉不到）", probe)
	}
	if probe.reason == "" {
		t.Fatal("reason 为空, want 非空（下载超时）")
	}
	// 15 张 × 100ms 单张时限本会拖 1.5s，总预算 1s 必须截停。
	if elapsed >= 2*time.Second {
		t.Fatalf("elapsed = %v, want 在总预算（1s）附近返回而不是无限等待", elapsed)
	}
}

// 外部图床（非 GitHub 域名）匿名失败后不再带 App 令牌重试：bearer 是仓库
// 凭证，不能发给 PR 描述里的任意 host。
func TestReviewAssetsNoBearerForExternalHost(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	cached := &githubmedia.CachedMedia{Path: "/cache/shot.png"}
	media := &fakeReviewMedia{cached: cached, anonErr: errors.New("forbidden anon")}
	review := e.withT3(nil, media, func(context.Context, int64) (string, error) {
		return "installation-token-1", nil
	})
	body := "![界面](https://uiv.example.com/f/x.png)"

	_, assets, err := review.ensureAssets(ctx, repo, 71, "sha-ext", body, 42)
	if err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if media.anonCalls != 1 || media.bearerCalls != 0 {
		t.Fatalf("calls = anon:%d bearer:%d, want anon:1 bearer:0（外部图床不重试令牌）",
			media.anonCalls, media.bearerCalls)
	}
	// 落库的是中文可读原因，不是 "forbidden anon" 原文。
	if len(assets) != 1 || assets[0].StoragePath != "" || assets[0].Error != "无法连接图片服务器" {
		t.Fatalf("assets = %+v, want 失败行 + 中文原因", assets)
	}
}

// F29 回归：部署未配置截图下载面（s.media == nil）时，downloadScreenshot
// 返回独立的 errReviewMediaUnconfigured（文案「截图下载面未配置」），与
// 「图床连不上」区分——用户不会去查网络。注意 withT3 会把 nil media 换成
// 环境的默认桩，这里必须直接 NewReviewService 构造 media 为 nil 的实例。
func TestDownloadScreenshotUnconfiguredMedia(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: e.fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, nil, nil, e.svc.cfg, zap.NewNop())

	_, err := review.downloadScreenshot(context.Background(), 1, "https://uiv.example.com/f/x.png")
	if !errors.Is(err, errReviewMediaUnconfigured) {
		t.Fatalf("err = %v, want errReviewMediaUnconfigured", err)
	}
	if got := screenshotErrorText(err); got != "截图下载面未配置" {
		t.Fatalf("screenshotErrorText = %q, want 截图下载面未配置", got)
	}
}

// F33：bearer 重试只发生在「可能靠令牌解决」的匿名失败上——GitHub 媒体域的
// 401/403/404（私有附件对匿名请求正是 404/403）。确定性失败（协议不支持、
// 内容不是图片、超过大小上限、内网地址、其它状态码）换令牌也一样失败，
// 不白铸令牌。
func TestDownloadScreenshotBearerRetryOnlyOnAuthFailure(t *testing.T) {
	e := newReviewEngineEnv(t)
	mediaURL := "https://github.com/user-attachments/assets/xyz"
	bearer := func(context.Context, int64) (string, error) { return "installation-token-1", nil }

	deterministic := []struct {
		name string
		err  error
	}{
		{"协议不支持", githubmedia.ErrInvalidMediaURL},
		{"内容不是图片", githubmedia.ErrUnsupportedUpstreamMedia},
		{"超过大小上限", githubmedia.ErrMediaTooLarge},
		{"内网地址", githubmedia.ErrBlockedUpstreamAddress},
		{"其它状态码", &githubmedia.UpstreamStatusError{StatusCode: 503}},
	}
	for _, c := range deterministic {
		media := &fakeReviewMedia{cached: &githubmedia.CachedMedia{Path: "/cache/shot.png"}, anonErr: c.err}
		review := e.withT3(nil, media, bearer)
		if _, err := review.downloadScreenshot(context.Background(), 42, mediaURL); !errors.Is(err, c.err) {
			t.Fatalf("%s: err = %v, want 原样返回匿名错误", c.name, err)
		}
		if media.anonCalls != 1 || media.bearerCalls != 0 {
			t.Fatalf("%s: calls = anon:%d bearer:%d, want anon:1 bearer:0（不铸令牌）",
				c.name, media.anonCalls, media.bearerCalls)
		}
	}

	// 私有附件匿名 404：带令牌重试，令牌传到下载面那一路。
	media := &fakeReviewMedia{cached: &githubmedia.CachedMedia{Path: "/cache/shot.png"},
		anonErr: &githubmedia.UpstreamStatusError{StatusCode: 404}}
	review := e.withT3(nil, media, bearer)
	got, err := review.downloadScreenshot(context.Background(), 42, mediaURL)
	if err != nil {
		t.Fatalf("404 + bearer 重试: %v", err)
	}
	if media.anonCalls != 1 || media.bearerCalls != 1 || media.lastBearer != "installation-token-1" {
		t.Fatalf("calls = anon:%d bearer:%d(%q), want anon:1 bearer:1 且带令牌",
			media.anonCalls, media.bearerCalls, media.lastBearer)
	}
	if got.Path != "/cache/shot.png" {
		t.Fatalf("cached = %+v", got)
	}
}

// timeoutErr 假 net.Error：45s 客户端超时以 Timeout() 形态到达（*httpError
// 没有 Unwrap，errors.Is 认不出 context.DeadlineExceeded）。
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "http client timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestScreenshotErrorText(t *testing.T) {
	urlErr := &url.Error{Op: "Get", URL: "https://uiv.example.com/f/x.png", Err: errors.New("dial tcp: i/o timeout")}
	deadlineURL := &url.Error{Op: "Get", URL: "https://uiv.example.com/f/x.png", Err: context.DeadlineExceeded}
	// F19 回归：生产里 Dialer.Control 抛出的 sentinel 会被 *net.OpError →
	// *url.Error 包住，外层 Error() 是
	// 「Get https://…: dial tcp 169.254.169.254:443: 图片地址指向内网或保留地址」
	// ——落库/上状态必须是 sentinel 自己的文案，英文原文与 URL 不透出。
	wrappedBlocked := &url.Error{
		Op:  "Get",
		URL: "https://evil.example.com/x.png",
		Err: &net.OpError{Op: "dial", Err: githubmedia.ErrBlockedUpstreamAddress},
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"网络错误不给原文", urlErr, "无法连接图片服务器"},
		{"包在 url.Error 里的超时也算超时", deadlineURL, "下载超时"},
		// F30 回归：同步被新快照推进、进程关停、上层超时的取消都以
		// context.Canceled 到达，不能落到兜底「无法连接图片服务器」。
		{"包在 url.Error 里的取消算被取消", &url.Error{Op: "Get", URL: "https://uiv.example.com/f/x.png", Err: context.Canceled}, "下载被取消"},
		{"裸 Canceled", fmt.Errorf("download: %w", context.Canceled), "下载被取消"},
		{"裸 DeadlineExceeded", fmt.Errorf("download: %w", context.DeadlineExceeded), "下载超时"},
		{"客户端超时（net.Error Timeout）", &url.Error{Op: "Get", URL: "https://uiv.example.com/f/x.png", Err: timeoutErr{}}, "下载超时"},
		{"地址哨兵原样透出", githubmedia.ErrBlockedUpstreamAddress, "图片地址指向内网或保留地址"},
		{"包装后的内网 sentinel 取 sentinel 文案", wrappedBlocked, "图片地址指向内网或保留地址"},
		{"包装后的非法地址 sentinel", fmt.Errorf("wrap: %w", githubmedia.ErrInvalidMediaURL), "不支持的图片地址"},
		{"空内容哨兵", githubmedia.ErrEmptyUpstreamMedia, "上游返回空内容"},
		{"超限哨兵", githubmedia.ErrMediaTooLarge, "图片超过大小上限"},
		{"下载面未配置", errReviewMediaUnconfigured, "截图下载面未配置"},
		{"上游状态码", &githubmedia.UpstreamStatusError{StatusCode: 503}, "上游返回状态码 503"},
		// E1 回归：本地缓存失败由 githubmedia 在产生点打上 ErrLocalCache，
		// service 层只认 sentinel、不再按 *fs.PathError / *os.SyscallError
		// 外壳嗅探。
		{"缓存写入 sentinel", githubmedia.ErrLocalCache, "截图缓存写入失败"},
		{"包装后的缓存写入 sentinel", fmt.Errorf("download: %w", githubmedia.ErrLocalCache), "截图缓存写入失败"},
		// P1 回归：网络 syscall 错误（connection refused 以 *os.SyscallError
		// 到达）必须归「无法连接图片服务器」——旧实现按 SyscallError 外壳
		// 嗅探会把它误归成「截图缓存写入失败」。
		{"网络 syscall 错误不按磁盘错误归类", &url.Error{Op: "Get", URL: "https://x/a.png", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}, "无法连接图片服务器"},
	}
	for _, c := range cases {
		if got := screenshotErrorText(c.err); got != c.want {
			t.Fatalf("%s: screenshotErrorText = %q, want %q", c.name, got, c.want)
		}
	}
	// 包装形态下逐项确认英文网络原文与 URL 没有透出（表格比较是全等，这里
	// 把要求写显式）。
	got := screenshotErrorText(wrappedBlocked)
	for _, banned := range []string{"Get ", "dial tcp", "evil.example.com"} {
		if strings.Contains(got, banned) {
			t.Fatalf("screenshotErrorText = %q, 不应含 %q", got, banned)
		}
	}
}

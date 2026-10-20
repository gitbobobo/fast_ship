package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GitHub 产出（INT-135）：sync_pending 的 PR 总账逐行同步到 GitHub——
// commit status（代码审查/视觉审查两个固定 context）、一条合并顶层评论、
// 每组合一条行内 review、视觉衔接。失败只写 sync_error 下轮重试；
// 成功后按起始 epoch 条件清 sync_pending。

const (
	reviewCodeStatusContext   = "代码审查"
	reviewVisualStatusContext = "视觉审查"
	reviewCodeCommentMarker   = "<!-- fast-ship-code-review -->"
	reviewVisualCommentMarker = "<!-- fast-ship-visual-review -->"
)

// statusDesc commit status 的 description 截断（GitHub 上限 140 字节）：
// 按 rune 边界累计到 ≤137 字节再加 "..."——按字节下标切会劈断中文等
// 多字节 UTF-8 序列，产出非法字符串被 GitHub 拒收。
func statusDesc(s string) string {
	const max = 140
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s { // i 是各 rune 的起始字节下标；s[:i] 恒在字符边界
		if i > max-3 {
			break
		}
		cut = i
	}
	return s[:cut] + "..."
}

// lockForPR 每 PR 一把进程内锁。
func (s *ReviewService) lockForPR(repo string, prNumber int) *sync.Mutex {
	key := repo + "#" + strconv.Itoa(prNumber)
	s.prLocksMu.Lock()
	defer s.prLocksMu.Unlock()
	lock, ok := s.prLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.prLocks[key] = lock
	}
	return lock
}

// syncPendingPRs 逐行处理 sync_pending 的 PR 总账。只处理项目设置仍 enabled
// 的账本行：被接管/停用项目的残留行不再发 GitHub 产出——同一仓库的
// 状态与评论由当前 enabled 项目独占。
func (s *ReviewService) syncPendingPRs(now time.Time) {
	var rows []model.ReviewPullRequest
	if err := s.db.Where(`sync_pending AND project_id IN (
		SELECT project_id FROM project_review_settings WHERE enabled)`).
		Order("updated_at ASC").Limit(reviewStageBatch).
		Find(&rows).Error; err != nil {
		s.logger.Error("查询待同步 PR 失败", zap.Error(err))
		return
	}
	for i := range rows {
		s.syncOnePR(&rows[i])
	}
}

// syncOnePR 一个 PR 的 GitHub 同步（同 PR 串行）。
// epoch 快照自进入时；失败写 sync_error（epoch 守卫），成功按 epoch 清 pending。
func (s *ReviewService) syncOnePR(ledger *model.ReviewPullRequest) {
	key := ledger.RepoFullName + "#" + strconv.Itoa(ledger.PRNumber)
	lock := s.lockForPR(ledger.RepoFullName, ledger.PRNumber)
	lock.Lock()
	defer func() {
		// 摘除锁条目：syncOnePR 只被单进程调度循环串行调用，不存在等在同
		// 一把锁上的并发 goroutine——本轮处理完即摘除，prLocks 大小只随
		// "正在同步的 PR"增长而不是随历史 PR 数无界增长。
		s.prLocksMu.Lock()
		if s.prLocks[key] == lock {
			delete(s.prLocks, key)
		}
		s.prLocksMu.Unlock()
		lock.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var cur model.ReviewPullRequest
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).First(&cur).Error; err != nil {
		return
	}
	epoch := cur.SyncEpoch
	now := time.Now().UTC()

	syncErr := s.doSyncPR(ctx, &cur, epoch, now)
	if syncErr != nil {
		s.db.Model(&model.ReviewPullRequest{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
				cur.ProjectID, cur.RepoFullName, cur.PRNumber, epoch).
			Updates(map[string]any{"sync_error": syncErr.Error(), "updated_at": now})
		s.logger.Warn("PR 审查同步失败（下轮重试）",
			zap.String("repo", cur.RepoFullName), zap.Int("pr", cur.PRNumber), zap.Error(syncErr))
		return
	}
	s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
			cur.ProjectID, cur.RepoFullName, cur.PRNumber, epoch).
		Updates(map[string]any{"sync_pending": false, "sync_error": "", "updated_at": now})
}

// doSyncPR 同步主体：读 PR 实时数据 → 代码汇总状态/评论/行内 → 视觉衔接。
// epoch 为进入 syncOnePR 时捕获的总账快照；所有中段写库带 sync_epoch 条件
// ——同步期间总账被推进（新 webhook/重跑/取消）时条件不命中，立即放弃本
// 轮落库，陈旧快照的结论不得写回（sync_pending 留给新 epoch 的下轮重试）。
func (s *ReviewService) doSyncPR(ctx context.Context, ledger *model.ReviewPullRequest, epoch int, now time.Time) error {
	if ledger.HeadSHA == "" {
		return nil
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return err
	}
	pr, err := client.GetPullRequest(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 PR 失败: "+err.Error())
	}
	targetURL := pr.HTMLURL
	if targetURL == "" {
		targetURL = ledger.RepoFullName + "/pull/" + strconv.Itoa(ledger.PRNumber)
	}

	var codeRuns, visualRuns []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND round_number = ? AND kind = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA, ledger.RoundNumber, model.ReviewKindCode).
		Order("created_at ASC").Find(&codeRuns).Error; err != nil {
		return errs.ErrInternal
	}
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND round_number = ? AND kind = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA, ledger.RoundNumber, model.ReviewKindVisual).
		Order("created_at ASC").Find(&visualRuns).Error; err != nil {
		return errs.ErrInternal
	}

	// 1. 代码 commit status（状态变化才写）。
	codeState, codeDesc := summarizeCodeRuns(codeRuns)
	if codeState != ledger.CodeStatus {
		if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
			ledger.HeadSHA, codeState, reviewCodeStatusContext, statusDesc(codeDesc), targetURL); err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "写代码审查状态失败: "+err.Error())
		}
		ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"code_status": codeState})
		if err != nil {
			return errs.ErrInternal
		}
		if !ok {
			return nil
		}
		ledger.CodeStatus = codeState
	}

	// 2. 视觉衔接决策（仅当全部代码运行终态后才有意义；视觉运行已建时走汇总）。
	visualCarry := false
	visualPending := len(visualRuns) == 0
	if len(visualRuns) == 0 {
		decision, fingerprint, priorVisuals, decErr := s.decideVisualForPR(ctx, ledger, pr, codeRuns)
		if decErr != nil {
			return decErr
		}
		switch decision {
		case visualStart:
			visualPending = true
			created, aborted, createErr := s.createVisualRuns(ledger, pr, codeRuns, now)
			if createErr != nil {
				return createErr
			}
			if aborted {
				return nil // 快照已被推进（新 webhook/重跑/取消），放弃本轮同步
			}
			visualRuns = created
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, "pending", reviewVisualStatusContext, statusDesc("视觉审查中"), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{
				"visual_status":              "pending",
				"visual_screens_fingerprint": fingerprint,
			})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = "pending"
			ledger.VisualScreensFingerprint = fingerprint
		case visualCarryFailure:
			visualPending = false
			visualCarry = true
			desc := visualCarryDescription(priorVisuals)
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, "failure", reviewVisualStatusContext, statusDesc(desc), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": "failure"})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = "failure"
			if err := s.upsertVisualComment(ctx, client, ledger, nil, priorVisuals, true, now); err != nil {
				return err
			}
		default:
			visualPending = false
		}
	} else {
		visualPending = false
	}

	// 3. 代码顶层评论（含视觉待办/遗留提示）。写之前复核 epoch：同步期间
	// 有新 SHA 进来时，旧快照的评论与行内 review 都不应再发。
	if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
		return errs.ErrInternal
	} else if !ok {
		return nil
	}
	codeBody := buildCodeComment(codeRuns, visualPending, visualCarry)
	if err := s.upsertPRComment(ctx, client, ledger, reviewCodeCommentMarker, codeBody, "code_comment_id", ledger.CodeCommentID); err != nil {
		return err
	}

	// 4. 行内 review：每个 completed 且未 posted 的代码运行，有效行发 review。
	if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
		return errs.ErrInternal
	} else if !ok {
		return nil
	}
	if err := s.postInlineReviews(ctx, client, ledger, codeRuns, now); err != nil {
		return err
	}

	// 5. 视觉运行存在时的状态与评论。
	if len(visualRuns) > 0 {
		if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
			return errs.ErrInternal
		} else if !ok {
			return nil
		}
		vs, vdesc := summarizeVisualRuns(visualRuns)
		if vs != ledger.VisualStatus {
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, vs, reviewVisualStatusContext, statusDesc(vdesc), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": vs})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = vs
		}
		if vs != "pending" {
			if err := s.upsertVisualComment(ctx, client, ledger, visualRuns, nil, false, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// updateLedgerEpoch 同步中段的总账写回：sync_epoch 守卫——命中返回 true；
// 不命中说明同步期间总账被推进（epoch 已变），本次快照作废，调用方应中止。
// ledgerEpochCurrent 同步中途复核总账 epoch：新 webhook 推进过就停止本次
// 剩余的 GitHub 写（评论/行内 review/视觉状态），避免把被取代 SHA 的结论
// 继续写出去。
func (s *ReviewService) ledgerEpochCurrent(ledger *model.ReviewPullRequest, epoch int) (bool, error) {
	var cur int
	if err := s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
		Select("sync_epoch").Scan(&cur).Error; err != nil {
		return false, err
	}
	return cur == epoch, nil
}

func (s *ReviewService) updateLedgerEpoch(ledger *model.ReviewPullRequest, epoch int, fields map[string]any) (bool, error) {
	res := s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, epoch).
		Updates(fields)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// upsertPRComment 优先按已存 comment id 直接更新（savedID）；更新撞 404
// （评论被删）时回退 marker 分页找回，仍找不到才创建。写回 comment id 列。
func (s *ReviewService) upsertPRComment(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, marker, body, idColumn string, savedID int64) error {
	commentID := savedID
	found := false
	if savedID != 0 {
		found = true
		if err := client.UpdatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, savedID, body); err != nil {
			if githubapi.IsNotFound(err) {
				found = false // 已存评论被删，回退按 marker 找回
			} else {
				return errs.New(errs.ErrGitHubAPI.Code, "更新顶层评论失败: "+err.Error())
			}
		} else {
			return nil // id 不变，无需写回
		}
	}
	if !found {
		var err error
		commentID, found, err = client.FindPullRequestComment(ctx, ledger.InstallationID,
			ledger.RepoFullName, ledger.PRNumber, marker)
		if err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "查找顶层评论失败: "+err.Error())
		}
		if found {
			if err := client.UpdatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, commentID, body); err != nil {
				if githubapi.IsNotFound(err) {
					found = false
				} else {
					return errs.New(errs.ErrGitHubAPI.Code, "更新顶层评论失败: "+err.Error())
				}
			}
		}
	}
	if !found {
		newID, err := client.CreatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber, body)
		if err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "创建顶层评论失败: "+err.Error())
		}
		commentID = newID
	}
	s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
		Update(idColumn, commentID)
	return nil
}

// upsertVisualComment 视觉顶层评论：本 head 的视觉运行结论或沿用提醒。
func (s *ReviewService) upsertVisualComment(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, visualRuns []model.ReviewRun, priorRuns []model.ReviewRun, carried bool, now time.Time) error {
	body := buildVisualComment(visualRuns, priorRuns, carried)
	return s.upsertPRComment(ctx, client, ledger, reviewVisualCommentMarker, body, "visual_comment_id", ledger.VisualCommentID)
}

// summarizeCodeRuns 代码审查汇总状态（status 描述 ≤140 字符由调用方截断）。
func summarizeCodeRuns(runs []model.ReviewRun) (state, description string) {
	if len(runs) == 0 {
		return "pending", "审查中"
	}
	for _, r := range runs {
		if !r.Status.Terminal() {
			return "pending", "审查中"
		}
	}
	for _, r := range runs {
		if r.Status != model.ReviewRunCompleted {
			return "error", "审查未完成：" + runCombinationLabel(&r) + " 失败"
		}
	}
	counts := map[string]int{"P0": 0, "P1": 0, "P2": 0}
	for _, r := range runs {
		var result api.ReviewCodeResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			switch f.Level {
			case api.P0:
				counts["P0"]++
			case api.P1:
				counts["P1"]++
			case api.P2:
				counts["P2"]++
			}
		}
	}
	total := counts["P0"] + counts["P1"] + counts["P2"]
	if total > 0 {
		var parts []string
		for _, lvl := range []string{"P0", "P1", "P2"} {
			if counts[lvl] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d 条", lvl, counts[lvl]))
			}
		}
		return "failure", "发现问题：" + strings.Join(parts, "，")
	}
	return "success", "审查通过"
}

// summarizeVisualRuns 视觉审查汇总：active → pending；未完成的运行 → error；
// blocker/worth_fixing → failure；否则 success。
func summarizeVisualRuns(runs []model.ReviewRun) (state, description string) {
	for _, r := range runs {
		if !r.Status.Terminal() {
			return "pending", "视觉审查中"
		}
	}
	for _, r := range runs {
		if r.Status != model.ReviewRunCompleted {
			return "error", "视觉审查未完成：" + runCombinationLabel(&r) + " 失败"
		}
	}
	n := 0
	for _, r := range runs {
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
				n++
			}
		}
	}
	if n > 0 {
		return "failure", fmt.Sprintf("发现 %d 条问题", n)
	}
	return "success", "审查通过"
}

// buildCodeComment 代码审查顶层评论正文：marker + 按组合分节 + 发现排序
// P0→P1→P2→Note + 视觉待办/遗留提示 + 复审说明。
func buildCodeComment(codeRuns []model.ReviewRun, visualPending, visualCarryFailure bool) string {
	var b strings.Builder
	b.WriteString(reviewCodeCommentMarker + "\n代码审查结论：\n\n")
	allCompleted, hasFindings := true, false
	for _, r := range codeRuns {
		fmt.Fprintf(&b, "### %s\n", runCombinationLabel(&r))
		switch {
		case r.Status == model.ReviewRunCompleted:
			var result api.ReviewCodeResult
			if json.Unmarshal([]byte(r.ResultJSON), &result) == nil {
				if result.Summary != "" {
					b.WriteString(result.Summary + "\n")
				}
				sort.SliceStable(result.Findings, func(i, j int) bool {
					return codeLevelRank(result.Findings[i].Level) < codeLevelRank(result.Findings[j].Level)
				})
				for _, f := range result.Findings {
					hasFindings = hasFindings || f.Level != api.Note
					loc := f.File
					if f.Line != nil && *f.Line > 0 {
						loc = fmt.Sprintf("%s:%d", f.File, *f.Line)
					}
					fmt.Fprintf(&b, "- %s %s %s\n", f.Level, loc, f.Body)
				}
			}
		case r.Status.Terminal():
			allCompleted = false
			fmt.Fprintf(&b, "- 本组合未完成（%s）：%s\n", r.Status, r.ErrorMessage)
		default:
			allCompleted = false
			b.WriteString("- 审查中…\n")
		}
		b.WriteString("\n")
	}
	if allCompleted && !hasFindings {
		b.WriteString("本轮未发现 P0/P1/P2 问题。\n\n")
	}
	if visualPending {
		b.WriteString("视觉审查尚未结束，将复核截图。\n\n")
	}
	if visualCarryFailure {
		b.WriteString("截图复核意见尚未处理\n\n")
	}
	b.WriteString("复审说明：推送新提交后会自动重新审查。")
	return b.String()
}

func codeLevelRank(level api.ReviewCodeFindingLevel) int {
	switch level {
	case api.P0:
		return 0
	case api.P1:
		return 1
	case api.P2:
		return 2
	}
	return 3
}

// buildVisualComment 视觉审查顶层评论：按严重度排序，带截图标签/维度/改法；
// carried 时正文说明沿用了上一轮（截图未变）。
func buildVisualComment(visualRuns, priorRuns []model.ReviewRun, carried bool) string {
	var b strings.Builder
	b.WriteString(reviewVisualCommentMarker + "\n视觉审查意见：\n\n")
	if carried {
		b.WriteString("沿用上一轮视觉审查结论（截图未变化，意见尚未处理）。\n\n")
	}
	source := visualRuns
	if carried {
		source = priorRuns
	}
	rank := func(s api.ReviewVisualFindingSeverity) int {
		switch s {
		case api.Blocker:
			return 0
		case api.WorthFixing:
			return 1
		}
		return 2
	}
	for _, r := range source {
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		fmt.Fprintf(&b, "### %s\n", runCombinationLabel(&r))
		if result.Summary != "" {
			b.WriteString(result.Summary + "\n")
		}
		sort.SliceStable(result.Findings, func(i, j int) bool {
			return rank(result.Findings[i].Severity) < rank(result.Findings[j].Severity)
		})
		for _, f := range result.Findings {
			fmt.Fprintf(&b, "- %s · %s · %s：%s\n  改法：%s\n", f.Severity, f.Screenshot, f.Dimension, f.Body, f.Fix)
		}
		for _, u := range result.Unmeasurable {
			fmt.Fprintf(&b, "- 无法判断 · %s：%s\n", u.Screenshot, u.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// postInlineReviews 每个 completed 且 posted_at IS NULL 的代码运行：
// P0–P2 且行号落在 diff 右侧有效行内 → SubmitReview；posted_at=now。
func (s *ReviewService) postInlineReviews(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, codeRuns []model.ReviewRun, now time.Time) error {
	var pending []model.ReviewRun
	for _, r := range codeRuns {
		if r.Status == model.ReviewRunCompleted && r.PostedAt == nil {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	diff, err := client.GetPullRequestDiff(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 diff 失败: "+err.Error())
	}
	valid := parseDiffRightLines(diff)
	for _, r := range pending {
		// 复核运行仍是 completed：同步期间新 SHA 会把旧运行置 cancelled，
		// 内存里的 codeRuns 是快照，不能据此继续发旧结论。
		var status model.ReviewRunStatus
		if err := s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
			Pluck("status", &status).Error; err != nil {
			return errs.ErrInternal
		}
		if status != model.ReviewRunCompleted {
			continue
		}
		var result api.ReviewCodeResult
		_ = json.Unmarshal([]byte(r.ResultJSON), &result)
		var comments []githubapi.ReviewInlineComment
		for _, f := range result.Findings {
			if f.Level == api.Note || f.Line == nil || *f.Line < 1 {
				continue
			}
			if lines, ok := valid[f.File]; ok && lines[*f.Line] {
				comments = append(comments, githubapi.ReviewInlineComment{
					Path: f.File, Line: *f.Line, Side: "RIGHT",
					Body: fmt.Sprintf("%s：%s", f.Level, f.Body),
				})
			}
		}
		var reviewID int64
		if len(comments) > 0 {
			id, err := client.SubmitReview(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber,
				githubapi.ReviewInput{Event: "COMMENT", Body: r.Summary, CommitID: ledger.HeadSHA, Comments: comments})
			if err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "提交行内 review 失败: "+err.Error())
			}
			reviewID = id
		}
		s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
			Updates(map[string]any{"posted_at": now, "review_id": reviewID, "updated_at": now})
	}
	return nil
}

// parseDiffRightLines 解析 unified diff：返回每个文件在 HEAD 右侧可被行内
// 评论的有效行号集合（hunk 内的 context 与 + 行）。
func parseDiffRightLines(diff string) map[string]map[int]bool {
	valid := map[string]map[int]bool{}
	var curFile string
	var newLine int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			curFile = strings.TrimPrefix(strings.TrimSpace(line), "+++ ")
			curFile = strings.TrimPrefix(curFile, "b/")
			if curFile == "/dev/null" {
				curFile = ""
			}
		case strings.HasPrefix(line, "@@ "):
			// @@ -a,b +c,d @@ — 取 +c 为新侧起始行。
			if idx := strings.Index(line, "+"); idx >= 0 {
				rest := line[idx+1:]
				num := rest
				if cut := strings.IndexAny(rest, ", "); cut >= 0 {
					num = rest[:cut]
				}
				newLine, _ = strconv.Atoi(strings.TrimSpace(num))
				newLine-- // hunk 首行自身先预减，循环里 ++ 后即 c
			}
		case curFile != "" && strings.HasPrefix(line, "+"):
			newLine++
			if valid[curFile] == nil {
				valid[curFile] = map[int]bool{}
			}
			valid[curFile][newLine] = true
		case curFile != "" && strings.HasPrefix(line, "-"):
			// 删除行不占新侧行号
		case curFile != "" && strings.HasPrefix(line, " "):
			// context 行才占新侧行号；diff 末尾的空行（结尾 \n 分裂出的
			// 空串）不是右侧有效行，不能计数。
			newLine++
			if valid[curFile] == nil {
				valid[curFile] = map[int]bool{}
			}
			valid[curFile][newLine] = true
		}
	}
	return valid
}

// ------ 视觉衔接 ------

type visualDecision int

const (
	visualNone         visualDecision = iota // 不启动
	visualStart                              // 建视觉运行
	visualCarryFailure                       // 不新建，沿用旧 failure 到新 head
)

// visualDecisionInput decideVisual 的纯输入（便于单测）。
type visualDecisionInput struct {
	CodeAllTerminalOK           bool // 全部代码运行 completed 且无 P0/P1/P2
	HasVisualCombos             bool
	HasScreenshots              bool
	AnyUIChanged                bool
	ScreensChanged              bool // 本次截图指纹 ≠ 总账 fingerprint
	AnyUIChangedSinceLastVisual bool
	PriorVisualFailed           bool // 上一轮视觉含 blocker/worth_fixing
}

// decideVisual 视觉衔接判定：
// 代码未全净、无视觉组合、无截图、无 UI 变化 → 不启动；
// 截图变了或上轮视觉后又有 UI 改动 → 启动；
// 截图未变且上一轮视觉有遗留 → 沿用 failure 到新 head。
func decideVisual(in visualDecisionInput) visualDecision {
	if !in.CodeAllTerminalOK || !in.HasVisualCombos || !in.HasScreenshots || !in.AnyUIChanged {
		return visualNone
	}
	if in.ScreensChanged || in.AnyUIChangedSinceLastVisual {
		return visualStart
	}
	if in.PriorVisualFailed {
		return visualCarryFailure
	}
	return visualNone
}

// decideVisualForPR 组装真实输入并调 decideVisual；返回决策、本次截图指纹、
// 上一轮视觉运行（沿用场景用）。
func (s *ReviewService) decideVisualForPR(ctx context.Context, ledger *model.ReviewPullRequest,
	pr *githubapi.PullRequest, codeRuns []model.ReviewRun) (visualDecision, string, []model.ReviewRun, error) {
	in := visualDecisionInput{CodeAllTerminalOK: true}
	anyUI, anySinceLast := false, false
	for _, r := range codeRuns {
		if r.Status != model.ReviewRunCompleted {
			in.CodeAllTerminalOK = false
			continue
		}
		var result api.ReviewCodeResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			in.CodeAllTerminalOK = false
			continue
		}
		for _, f := range result.Findings {
			if f.Level == api.P0 || f.Level == api.P1 || f.Level == api.P2 {
				in.CodeAllTerminalOK = false
			}
		}
		anyUI = anyUI || result.UiChanged
		anySinceLast = anySinceLast || result.UiChangedSinceLastVisual
	}
	in.AnyUIChanged = anyUI
	in.AnyUIChangedSinceLastVisual = anySinceLast

	// 视觉组合是否配置。
	var settings model.ProjectReviewSettings
	if err := s.db.Where("project_id = ?", ledger.ProjectID).First(&settings).Error; err != nil {
		return visualNone, "", nil, errs.ErrInternal
	}
	visualCombos, err := parseCombinations(settings.VisualCombinationsJSON)
	if err != nil {
		return visualNone, "", nil, errs.ErrInternal
	}
	in.HasVisualCombos = len(visualCombos) > 0

	refs := collectScreenshotURLs(pr.Body)
	in.HasScreenshots = len(refs) > 0
	fingerprint := screenshotFingerprint(refs)
	in.ScreensChanged = fingerprint != ledger.VisualScreensFingerprint

	// 上一轮（更早 round）视觉运行是否带 blocker/worth_fixing。历史只看
	// 本项目账本——其他项目同仓库的残留运行不构成「上一轮」。
	var allPrior []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND kind = ? AND round_number < ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, model.ReviewKindVisual, ledger.RoundNumber).
		Order("round_number DESC, created_at ASC").Find(&allPrior).Error; err != nil {
		return visualNone, "", nil, errs.ErrInternal
	}
	lastRound := 0
	var priorRuns []model.ReviewRun
	for _, r := range allPrior {
		if lastRound == 0 {
			lastRound = r.RoundNumber
		}
		if r.RoundNumber != lastRound {
			break // 只看最近一轮
		}
		priorRuns = append(priorRuns, r)
		if r.Status == model.ReviewRunCompleted && visualResultHasFailure(r.ResultJSON) {
			in.PriorVisualFailed = true
		}
	}
	return decideVisual(in), fingerprint, priorRuns, nil
}

// visualResultHasFailure 视觉结论是否含 blocker/worth_fixing。
func visualResultHasFailure(raw string) bool {
	var result api.ReviewVisualResult
	if json.Unmarshal([]byte(raw), &result) != nil {
		return false
	}
	for _, f := range result.Findings {
		if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
			return true
		}
	}
	return false
}

// visualCarryDescription 沿用旧视觉 failure 时的 status 描述。
func visualCarryDescription(priorRuns []model.ReviewRun) string {
	n := 0
	lastRound := 0
	for _, r := range priorRuns {
		if lastRound == 0 {
			lastRound = r.RoundNumber
		}
		if r.RoundNumber != lastRound {
			break
		}
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
				n++
			}
		}
	}
	if n == 0 {
		return "截图复核意见尚未处理"
	}
	return fmt.Sprintf("截图复核意见尚未处理（上轮 %d 条）", n)
}

// createVisualRuns 为当前 round 建视觉运行（每组合一行，queued 等提升）。
// 事务内先复核快照：同步期间总账被推进（新 webhook、手动重跑）或该 head
// 的代码运行被取消时，建出来的是陈旧视觉运行——aborted=true 让调用方
// 放弃本轮同步（新 sync_pending 会重试）。
func (s *ReviewService) createVisualRuns(ledger *model.ReviewPullRequest,
	pr *githubapi.PullRequest, codeRuns []model.ReviewRun, now time.Time) ([]model.ReviewRun, bool, error) {
	var settings model.ProjectReviewSettings
	if err := s.db.Where("project_id = ?", ledger.ProjectID).First(&settings).Error; err != nil {
		return nil, false, errs.ErrInternal
	}
	combos, err := parseCombinations(settings.VisualCombinationsJSON)
	if err != nil {
		return nil, false, errs.ErrInternal
	}
	var runs []model.ReviewRun
	aborted := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var cur model.ReviewPullRequest
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
			First(&cur).Error; err != nil {
			return err
		}
		if cur.HeadSHA != ledger.HeadSHA || cur.RoundNumber != ledger.RoundNumber ||
			cur.SyncEpoch != ledger.SyncEpoch {
			aborted = true
			return nil
		}
		var cancelled int64
		if err := tx.Model(&model.ReviewRun{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND kind = ? AND status = ?",
				ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA,
				model.ReviewKindCode, model.ReviewRunCancelled).
			Count(&cancelled).Error; err != nil {
			return err
		}
		if cancelled > 0 {
			aborted = true
			return nil
		}
		for i := range combos {
			c := &combos[i]
			t3ProjectID := ""
			if c.T3ProjectId != nil {
				t3ProjectID = *c.T3ProjectId
			}
			run := &model.ReviewRun{
				ID:               uuid.NewString(),
				UserID:           ledger.UserID,
				ProjectID:        ledger.ProjectID,
				Kind:             model.ReviewKindVisual,
				RepoFullName:     ledger.RepoFullName,
				PRNumber:         ledger.PRNumber,
				HeadSHA:          ledger.HeadSHA,
				RoundNumber:      ledger.RoundNumber,
				InstallationID:   ledger.InstallationID,
				TriggerAction:    "visual",
				PRTitle:          pr.Title,
				PRURL:            pr.HTMLURL,
				BaseRef:          pr.BaseRef,
				HeadRef:          pr.HeadRef,
				EnvironmentID:    c.EnvironmentId,
				T3ProjectID:      t3ProjectID,
				InstanceID:       c.InstanceId,
				Model:            c.Model,
				ModelOptionsJSON: canonicalOptionsJSON(c.Options),
				ThreadID:         uuid.NewString(),
				LaunchCommandID:  uuid.NewString(),
				InitialMessageID: uuid.NewString(),
				Status:           model.ReviewRunQueued,
				DeadlineAt:       now,
				WorktreeState:    model.ReviewWorktreeNone,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run)
			if res.Error != nil {
				return res.Error
			}
			runs = append(runs, *run)
		}
		return s.promoteQueuedRunsTx(tx, ledger.UserID, now)
	})
	if err != nil {
		return nil, false, errs.ErrInternal
	}
	return runs, aborted, nil
}

// screenshotFingerprint 排序后 URL 列表的 SHA-256 前 16 位（hex）。
func screenshotFingerprint(refs []screenshotRef) string {
	urls := make([]string, 0, len(refs))
	for _, r := range refs {
		urls = append(urls, r.URL)
	}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

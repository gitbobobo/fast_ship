package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PR 评审的事务外同步（INT-93）：评论/check 推送与取消线程的中断。全部在
// 调度循环（或测试）里驱动，事务内绝不调用。每个 PR 一把进程内锁——同一 PR
// 的评论更新串行，避免交错写。

// prReviewSyncBatchLimit 单轮 tick 最多同步的 PR 数。
const prReviewSyncBatchLimit = 20

// prThreadStopper 取消路径的中断出口（T3ThreadsService.StopThread 同签名）。
// 接口化便于测试注入假实现。
type prThreadStopper interface {
	StopThread(ctx context.Context, userID, envID, threadID string) (*T3ThreadStopResult, error)
}

// withPRLock 进程内 per-PR 互斥（单进程部署，不引入分布式锁）。
func (s *PRReviewService) withPRLock(repo string, pr int, fn func()) {
	s.prLocksMu.Lock()
	key := repo + "#" + itoa10(pr)
	mu, ok := s.prLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		s.prLocks[key] = mu
	}
	s.prLocksMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	fn()
}

func itoa10(n int) string { return fmt.Sprintf("%d", n) }

// SyncPendingReviews 调度步骤：把 sync_pending 的 PR 推到 GitHub（评论 + check）。
// 失败只记 sync_error，下轮重试；不改运行终态。GitHub 身份按评审账行的验签
// 身份解析（INT-119）：某身份未配置 App 时该行的同步报错重试，不影响其他身份。
func (s *PRReviewService) SyncPendingReviews(now time.Time) {
	s.stopCancelledPRThreads(now)

	pending, err := s.reviews.ListPendingSyncTx(s.db, prReviewSyncBatchLimit)
	if err != nil {
		s.logger.Error("查询待同步 PR 失败", zap.Error(err))
		return
	}
	for i := range pending {
		rev := pending[i]
		s.withPRLock(rev.RepoFullName, rev.PRNumber, func() {
			s.syncOnePR(&rev, now)
		})
	}
}

// stopCancelledPRThreads 取消线程：已取消且曾 dispatched/running 的运行向 T3
// 发 run.interrupt(holdQueue=true)；queued 与未认领的空串运行不动。每个运行
// 只尝试一次（stop_attempted_at 标记）。
func (s *PRReviewService) stopCancelledPRThreads(now time.Time) {
	if s.threads == nil {
		return
	}
	var runs []model.AutomationRun
	err := s.db.
		Where("status = ? AND trigger_type IN ? AND dispatch_started_at IS NOT NULL AND stop_attempted_at IS NULL",
			model.AutomationRunCancelled, prTriggerTypes).
		Limit(prReviewSyncBatchLimit).
		Find(&runs).Error
	if err != nil {
		s.logger.Error("查询已取消的 PR 运行失败", zap.Error(err))
		return
	}
	for i := range runs {
		run := runs[i]
		s.withPRLock(run.RepoFullName, run.PRNumber, func() {
			s.stopOneCancelledRun(&run, now)
		})
	}
}

func (s *PRReviewService) stopOneCancelledRun(run *model.AutomationRun, now time.Time) {
	// 先占标记（条件 UPDATE 只允许一个执行者），再发中断；T3 调用在事务外。
	n, err := s.runs.UpdateColumnsWhere(context.Background(), run.ID,
		"status = ? AND stop_attempted_at IS NULL", []any{model.AutomationRunCancelled},
		map[string]any{"stop_attempted_at": now})
	if err != nil || n == 0 {
		if err != nil {
			s.logger.Error("标记取消中断失败", zap.String("run_id", run.ID), zap.Error(err))
		}
		return
	}
	if s.threads == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), automationPostCommandBudget)
	defer cancel()
	if _, err := s.threads.StopThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
		// 中断失败只记日志：运行已是终态 cancelled，GitHub 侧状态照常展示；
		// 线程若仍在跑，其回报会因令牌已消费而被拒。
		s.logger.Warn("中断已取消的审查线程失败",
			zap.String("run_id", run.ID), zap.String("thread_id", run.ThreadID), zap.Error(err))
		return
	}
	s.logger.Info("已中断被取代的审查线程",
		zap.String("run_id", run.ID), zap.String("thread_id", run.ThreadID))
}

// OnLaunchCancelled launchThreadForRun 的收尾：CreateThread 返回后立刻再读状态，
// 若已被取消（先停后建竞态）立刻 StopThread，不让孤儿线程继续跑。
func (s *PRReviewService) OnLaunchCancelled(run *model.AutomationRun, now time.Time) {
	// CreateThread 返回后才发现已取消：令牌可能刚签发、尚未被 webhook 消费。
	_ = s.runs.ConsumeRunTokensTx(s.db, []string{run.ID}, now)
	// 与 stopCancelledPRThreads 共用 stop_attempted_at：先占标记再中断，避免
	// 随后的 SyncPendingReviews 再发一次 run.interrupt。
	s.stopOneCancelledRun(run, now)
}

// ---- 评论 + check 同步 ----

// scopeRunsForRev 给按 (repo, pr) 查运行的查询加上评审行身份的过滤：运行的
// app_user_id 即开轮身份（INT-129 起恒为空串，存量行同值）——同键过滤保留，
// 存量多身份行不会被部署身份的同步误碰。
func scopeRunsForRev(q *gorm.DB, rev *model.GitHubPRReview) *gorm.DB {
	return q.Where("app_user_id = ?", rev.AppUserID)
}

// syncOnePR 同步一个 PR 身份行的机器人评论与 check runs。展示当前 HeadSHA 下
// 该身份的运行（同一 SHA 多规则共享一轮）。
func (s *PRReviewService) syncOnePR(rev *model.GitHubPRReview, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), automationPostCommandBudget)
	defer cancel()

	runs, err := s.runsForHeadSHA(rev)
	if err != nil {
		s.recordSyncError(rev, "查询运行失败: "+err.Error(), now)
		return
	}
	if len(runs) == 0 {
		// 当前 SHA 下行全部消失（规则/项目被删）：仅清快照帧，并发新轮保留 pending。
		_ = s.reviews.ClearPendingSync(context.Background(), rev, now)
		return
	}

	if err := s.ensureChecksAndPush(ctx, rev, runs); err != nil {
		s.recordSyncError(rev, err.Error(), now)
		return
	}
	// 旧 SHA 被取消的运行：check 收敛为 cancelled（一次同步即终态，重复更新幂等）。
	if err := s.pushCancelledOldSHAChecks(ctx, rev); err != nil {
		s.recordSyncError(rev, err.Error(), now)
		return
	}
	if err := s.reviews.ClearPendingSync(context.Background(), rev, now); err != nil {
		s.logger.Error("清除同步标记失败", zap.String("repo", rev.RepoFullName), zap.Error(err))
	}
}

// pushCancelledOldSHAChecks 把历史 SHA 里已取消且已有 check 的运行（限本身份）
// 推成 completed/cancelled（新 SHA 取代旧轮时旧 check 不能悬在 in_progress）。
func (s *PRReviewService) pushCancelledOldSHAChecks(ctx context.Context, rev *model.GitHubPRReview) error {
	q := scopeRunsForRev(s.db.
		Where("repo_full_name = ? AND pr_number = ? AND head_sha <> ? AND status = ? AND check_run_id IS NOT NULL",
			rev.RepoFullName, rev.PRNumber, rev.HeadSHA, model.AutomationRunCancelled), rev)
	var old []model.AutomationRun
	if err := q.Find(&old).Error; err != nil {
		return err
	}
	gh, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	for i := range old {
		in := checkUpdateForRun(old[i])
		if err := gh.UpdateCheckRun(ctx, rev.InstallationID, rev.RepoFullName, *old[i].CheckRunID, in); err != nil {
			if githubapi.IsForbidden(err) {
				s.logger.Warn("更新旧 SHA check 403，跳过其余 check",
					zap.String("repo", rev.RepoFullName), zap.Int("pr", rev.PRNumber))
				return nil
			}
			return fmt.Errorf("更新旧轮 check 失败: %w", err)
		}
	}
	return nil
}

func (s *PRReviewService) recordSyncError(rev *model.GitHubPRReview, msg string, now time.Time) {
	if err := s.reviews.SetSyncError(context.Background(), rev, truncate(msg, 500), now); err != nil {
		s.logger.Error("记录同步错误失败", zap.String("repo", rev.RepoFullName), zap.Error(err))
	}
}

// ensureChecksAndPush 推送 check runs 与机器人评论。评论/创建/更新任一失败整体
// 返回错误（sync_error 记录，下轮重试）。
func (s *PRReviewService) ensureChecksAndPush(ctx context.Context, rev *model.GitHubPRReview, runs []model.AutomationRun) error {
	if err := s.ensureCheckRuns(ctx, rev, runs); err != nil {
		return err
	}
	if err := s.pushComment(ctx, rev, runs); err != nil {
		return err
	}
	return s.pushCheckStates(ctx, rev, runs)
}

// ensureCheckRuns 为缺 check 的运行创建 check run（身份取评审行的验签身份：
// 该行的运行全部由它开轮）。GitHub 返回 403（Checks API 未授权）时退回整轮一
// 个 `Fast Ship review` 合并 check。合并 check 同样 403 时跳过 check（不再重
// 试同一端点），调用方仍推评论。
func (s *PRReviewService) ensureCheckRuns(ctx context.Context, rev *model.GitHubPRReview, runs []model.AutomationRun) error {
	mergedID, mergedUsed := s.roundMergedCheckID(runs)
	var pending []model.AutomationRun
	for i := range runs {
		if runs[i].CheckRunID != nil {
			continue
		}
		if runs[i].Status == model.AutomationRunCancelled && mergedUsed {
			continue // 合并 check 由聚合状态表达取消
		}
		pending = append(pending, runs[i])
	}
	if len(pending) == 0 {
		return nil
	}

	for i := range pending {
		run := &pending[i]
		gh, err := s.clientFor(ctx)
		if err != nil {
			return err
		}
		name := checkNameForModel(run.ModelName)
		id, err := gh.CreateCheckRun(ctx, rev.InstallationID, rev.RepoFullName, name, run.HeadSHA, run.ID)
		if err != nil {
			if !githubapi.IsForbidden(err) {
				return fmt.Errorf("创建 check run 失败: %w", err)
			}
			// 403 → 整轮退回一个合并 check。合并 check 若同样 403（App 没有
			// Checks 写权限），不再重试同一端点：跳过 check，让调用方继续推评论。
			s.logger.Warn("Checks API 403，退回整轮合并 check",
				zap.String("repo", rev.RepoFullName), zap.Int("pr", rev.PRNumber))
			if mergedID == 0 {
				id, err := gh.CreateCheckRun(ctx, rev.InstallationID, rev.RepoFullName,
					"Fast Ship review", run.HeadSHA, "round:"+itoa10(rev.RoundNumber))
				if err != nil {
					if githubapi.IsForbidden(err) {
						s.logger.Warn("Checks API 全部 403，跳过 check，仍同步评论",
							zap.String("repo", rev.RepoFullName), zap.Int("pr", rev.PRNumber))
						return nil
					}
					return fmt.Errorf("创建合并 check run 失败: %w", err)
				}
				mergedID = id
			}
			if err := s.assignRoundCheckID(rev, mergedID); err != nil {
				return err
			}
			return nil
		}
		if _, err := s.reviews.SaveRunCheckRunIDTx(s.db, run.ID, id); err != nil {
			return fmt.Errorf("写回 check run id 失败: %w", err)
		}
		// 同步局部视图，后续 pushCheckStates 用得到。
		run.CheckRunID = &id
	}
	return nil
}

// roundMergedCheckID 合并 check 的判定：本轮多个运行指向同一个 check id。
func (s *PRReviewService) roundMergedCheckID(runs []model.AutomationRun) (int64, bool) {
	counts := map[int64]int{}
	for i := range runs {
		if runs[i].CheckRunID != nil {
			counts[*runs[i].CheckRunID]++
		}
	}
	for id, n := range counts {
		if n > 1 {
			return id, true
		}
	}
	return 0, false
}

// assignRoundCheckID 本轮（限本身份）全部缺 check 的运行挂到合并 check。
func (s *PRReviewService) assignRoundCheckID(rev *model.GitHubPRReview, checkRunID int64) error {
	q := scopeRunsForRev(s.db.
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ? AND check_run_id IS NULL",
			rev.RepoFullName, rev.PRNumber, rev.HeadSHA), rev)
	var runs []model.AutomationRun
	if err := q.Find(&runs).Error; err != nil {
		return err
	}
	for i := range runs {
		if _, err := s.reviews.SaveRunCheckRunIDTx(s.db, runs[i].ID, checkRunID); err != nil {
			return err
		}
	}
	return nil
}

// pushCheckStates 按运行状态推送 check。同一 check id（合并）只推一次，状态取
// 本轮聚合。
func (s *PRReviewService) pushCheckStates(ctx context.Context, rev *model.GitHubPRReview, runs []model.AutomationRun) error {
	// 刷新内存视图（ensure 阶段可能写回了 id）。
	fresh, err := s.runsForHeadSHA(rev)
	if err != nil {
		return err
	}
	runs = fresh

	byCheck := map[int64][]model.AutomationRun{}
	var order []int64
	for i := range runs {
		if runs[i].CheckRunID == nil {
			continue
		}
		id := *runs[i].CheckRunID
		if _, ok := byCheck[id]; !ok {
			order = append(order, id)
		}
		byCheck[id] = append(byCheck[id], runs[i])
	}
	for _, id := range order {
		group := byCheck[id]
		// 同一 check id 的运行都由同一评审行开轮（查运行已按身份过滤），
		// 统一用部署唯一的 client。
		gh, err := s.clientFor(ctx)
		if err != nil {
			return err
		}
		if len(group) == 1 {
			run := group[0]
			in := checkUpdateForRun(run)
			if err := gh.UpdateCheckRun(ctx, rev.InstallationID, rev.RepoFullName, id, in); err != nil {
				if githubapi.IsForbidden(err) {
					s.logger.Warn("更新 check 403，跳过其余 check",
						zap.String("repo", rev.RepoFullName), zap.Int("pr", rev.PRNumber))
					return nil
				}
				return fmt.Errorf("更新 check run 失败: %w", err)
			}
			continue
		}
		in := checkUpdateForGroup(group)
		if err := gh.UpdateCheckRun(ctx, rev.InstallationID, rev.RepoFullName, id, in); err != nil {
			if githubapi.IsForbidden(err) {
				s.logger.Warn("更新合并 check 403，跳过其余 check",
					zap.String("repo", rev.RepoFullName), zap.Int("pr", rev.PRNumber))
				return nil
			}
			return fmt.Errorf("更新合并 check run 失败: %w", err)
		}
	}
	return nil
}

func (s *PRReviewService) runsForHeadSHA(rev *model.GitHubPRReview) ([]model.AutomationRun, error) {
	var runs []model.AutomationRun
	err := scopeRunsForRev(s.db.
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?", rev.RepoFullName, rev.PRNumber, rev.HeadSHA), rev).
		Order("model_name ASC, id ASC").
		Find(&runs).Error
	return runs, err
}

// checkNameForModel 每个模型一个 check：`Fast Ship review (<model>)`。
func checkNameForModel(modelName string) string {
	return "Fast Ship review (" + modelName + ")"
}

// checkUpdateForRun 单运行 → check 状态映射。
func checkUpdateForRun(run model.AutomationRun) githubapi.CheckUpdate {
	in := githubapi.CheckUpdate{
		Title:   checkNameForModel(run.ModelName),
		Summary: runStatusLabel(run.Status),
		Text:    checkTextForRun(run),
	}
	switch run.Status {
	case model.AutomationRunQueued:
		in.Status = githubapi.CheckQueued
	case model.RunStatusPendingRun, model.AutomationRunDispatched, model.AutomationRunRunning:
		in.Status = githubapi.CheckInProgress
	case model.AutomationRunCompleted:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionSuccess
	case model.AutomationRunFailed, model.AutomationRunTimedOut:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionFailure
	case model.AutomationRunCancelled:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionCancelled
	}
	return in
}

// checkUpdateForGroup 合并 check：全部终态才收敛；有失败即失败；全取消才取消。
func checkUpdateForGroup(runs []model.AutomationRun) githubapi.CheckUpdate {
	in := githubapi.CheckUpdate{
		Title:   "Fast Ship review",
		Summary: "整轮合并 check（Checks API 不可用时的退回形态）",
	}
	allDone, allCancelled, anyFailed, anyActive := true, true, false, false
	for _, run := range runs {
		switch run.Status {
		case model.AutomationRunQueued:
			allDone, allCancelled = false, false
		case model.RunStatusPendingRun, model.AutomationRunDispatched, model.AutomationRunRunning:
			allDone, allCancelled, anyActive = false, false, true
		case model.AutomationRunCompleted:
			allCancelled = false
		case model.AutomationRunFailed, model.AutomationRunTimedOut:
			// 失败/超时仍是终态：allDone 保持 true，否则会落到 queued/in_progress。
			allCancelled, anyFailed = false, true
		case model.AutomationRunCancelled:
			// 取消是终态，不把整轮判成还在跑。
		}
	}
	in.Text = "Head SHA: " + roundHeadSHA(runs)
	switch {
	case !allDone:
		if anyActive {
			in.Status = githubapi.CheckInProgress
		} else {
			in.Status = githubapi.CheckQueued
		}
	case anyFailed:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionFailure
	case allCancelled:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionCancelled
	default:
		in.Status = githubapi.CheckCompleted
		in.Conclusion = githubapi.ConclusionSuccess
	}
	return in
}

func roundHeadSHA(runs []model.AutomationRun) string {
	for _, run := range runs {
		if run.HeadSHA != "" {
			return run.HeadSHA
		}
	}
	return ""
}

func checkTextForRun(run model.AutomationRun) string {
	lines := []string{
		"运行 ID: " + run.ID,
		"模型: " + run.ModelName + "（" + run.ModelInstanceID + "）",
		"轮次: " + itoa10(run.RoundNumber),
	}
	if run.ErrorCode != "" {
		lines = append(lines, "错误: "+run.ErrorCode+" "+run.ErrorMessage)
	}
	if run.Summary != "" {
		lines = append(lines, "摘要: "+run.Summary)
	}
	return strings.Join(lines, "\n")
}

// runStatusLabel 运行状态在评论表格里的展示文案。
func runStatusLabel(status model.AutomationRunStatus) string {
	switch status {
	case model.AutomationRunQueued:
		return "排队中"
	case model.RunStatusPendingRun, model.AutomationRunDispatched, model.AutomationRunRunning:
		return "进行中"
	case model.AutomationRunCompleted:
		return "完成"
	case model.AutomationRunFailed, model.AutomationRunTimedOut:
		return "失败"
	case model.AutomationRunCancelled:
		return "已取消"
	}
	return string(status)
}

// prReviewCommentMarkerFor 按验签身份生成机器人评论的隐藏标记：config 身份
// 沿用旧标记（线上已有评论仍按它找回）；用户身份追加 app 后缀——所有身份共用
// 一个标记时，B 首次同步会把 A 的评论找回来改。
func prReviewCommentMarkerFor(appUserID string) string {
	if appUserID == "" {
		return prReviewCommentMarker
	}
	return "<!-- fast-ship-pr-review:app:" + appUserID + " -->"
}

// pushComment 每（PR，身份）一条机器人顶层评论：已保存 comment_id 则直接
// PATCH；404/未保存再按本身份的隐藏标记找回，失效则创建。只展示当前 SHA。评论
// 按评审行的验签身份推送（该行的运行全部由它开轮）。
func (s *PRReviewService) pushComment(ctx context.Context, rev *model.GitHubPRReview, runs []model.AutomationRun) error {
	gh, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	marker := prReviewCommentMarkerFor(rev.AppUserID)
	body := prReviewCommentBody(rev, runs)
	if rev.CommentID != 0 {
		if err := gh.UpdatePullRequestComment(ctx, rev.InstallationID, rev.RepoFullName, rev.CommentID, body); err == nil {
			return nil
		} else if !githubapi.IsNotFound(err) {
			return fmt.Errorf("更新机器人评论失败: %w", err)
		}
	}
	commentID, found, err := gh.FindPullRequestComment(ctx, rev.InstallationID, rev.RepoFullName, rev.PRNumber, marker)
	if err != nil {
		return fmt.Errorf("查找机器人评论失败: %w", err)
	}
	if found {
		if err := gh.UpdatePullRequestComment(ctx, rev.InstallationID, rev.RepoFullName, commentID, body); err != nil {
			return fmt.Errorf("更新机器人评论失败: %w", err)
		}
		if commentID != rev.CommentID {
			s.saveCommentID(rev, commentID)
		}
		return nil
	}
	created, err := gh.CreatePullRequestComment(ctx, rev.InstallationID, rev.RepoFullName, rev.PRNumber, body)
	if err != nil {
		return fmt.Errorf("创建机器人评论失败: %w", err)
	}
	s.saveCommentID(rev, created)
	return nil
}

// saveCommentID 找回/新建评论后把 ID 写回本身份的评审行（失败只留日志：下轮
// 同步还能按标记找回）。
func (s *PRReviewService) saveCommentID(rev *model.GitHubPRReview, commentID int64) {
	if err := s.db.Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ?", rev.RepoFullName, rev.PRNumber, rev.AppUserID).
		Update("comment_id", commentID).Error; err != nil {
		s.logger.Error("写回机器人评论 ID 失败", zap.String("repo", rev.RepoFullName), zap.Error(err))
	}
}

// prReviewCommentBody 机器人评论正文：表格展示模型、harness、状态、head SHA、
// 轮次。隐藏标记按评审行的验签身份生成（各身份的评论互不找回）。
func prReviewCommentBody(rev *model.GitHubPRReview, runs []model.AutomationRun) string {
	var b strings.Builder
	b.WriteString(prReviewCommentMarkerFor(rev.AppUserID) + "\n\n")
	b.WriteString(fmt.Sprintf("## Fast Ship 自动评审（第 %d 轮）\n\n", rev.RoundNumber))
	b.WriteString("| 模型 | Harness | 状态 | Head SHA | 轮次 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	sorted := append([]model.AutomationRun(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ModelName < sorted[j].ModelName })
	for _, run := range sorted {
		sha := run.HeadSHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %d |\n",
			run.ModelName, run.ModelInstanceID, runStatusLabel(run.Status), sha, run.RoundNumber))
	}
	return b.String()
}

// markPRReviewPendingTx PR 运行状态变化后置同步标记（超时/派发失败/结束回报
// 的事务末尾调用；非 PR 触发不置位）。只动 (repo, pr, app_user_id) 命中本运行
// 开轮身份的那一行——各身份行的同步只展示自己身份的运行，多余的置位只是一次
// 冗余推送，跨身份置位则会把别人的状态算进来。行缺失（正常路径开轮时必建）时
// 按本运行的身份与轮次补建。
func markPRReviewPendingTx(tx *gorm.DB, run *model.AutomationRun, now time.Time) error {
	if !run.TriggerType.IsPRTrigger() {
		return nil
	}
	res := tx.Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ?", run.RepoFullName, run.PRNumber, run.AppUserID).
		Updates(map[string]any{"sync_pending": true, "sync_epoch": gorm.Expr("sync_epoch + 1"), "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tx.Create(&model.GitHubPRReview{
			RepoFullName:   run.RepoFullName,
			PRNumber:       run.PRNumber,
			AppUserID:      run.AppUserID,
			InstallationID: run.InstallationID,
			RoundNumber:    run.RoundNumber,
			HeadSHA:        run.HeadSHA,
			SyncPending:    true,
			SyncEpoch:      1,
			UpdatedAt:      now,
		}).Error
	}
	return nil
}

// markReviewPendingAfterCancelTx 取消旧 SHA 后置同步标记（无需开新轮也要把
// 「已取消」推到评论/check）。被取消的运行必然属于验签身份（取消已按身份过
// 滤），只动该身份的行；行不存在时用被取消运行的最大轮次补建。
func markReviewPendingAfterCancelTx(tx *gorm.DB, repoFullName string, prNumber int, appUserID string, cancelled []string, now time.Time) error {
	if len(cancelled) == 0 {
		return nil
	}
	var runs []model.AutomationRun
	if err := tx.Where("id IN ?", cancelled).Find(&runs).Error; err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	maxRound, headSHA := 1, ""
	for i := range runs {
		if runs[i].RoundNumber > maxRound {
			maxRound = runs[i].RoundNumber
		}
		if headSHA == "" {
			headSHA = runs[i].HeadSHA
		}
	}
	res := tx.Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ?", repoFullName, prNumber, appUserID).
		Updates(map[string]any{"sync_pending": true, "sync_epoch": gorm.Expr("sync_epoch + 1"), "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tx.Create(&model.GitHubPRReview{
			RepoFullName:   repoFullName,
			PRNumber:       prNumber,
			AppUserID:      appUserID,
			InstallationID: runs[0].InstallationID,
			RoundNumber:    maxRound,
			HeadSHA:        headSHA,
			SyncPending:    true,
			SyncEpoch:      1,
			UpdatedAt:      now,
		}).Error
	}
	return nil
}

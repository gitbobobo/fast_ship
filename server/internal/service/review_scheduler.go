package service

import (
	"context"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PR 审查调度循环（INT-135）：单进程一个 10s tick + 非阻塞 kick。
// 每轮依次：到期防抖消费并开轮 → 排队提升 → 派发 → 超时 → 取消运行的线程
// 中断 → GitHub 同步 → 工作区清理。各阶段独立容错；网络调用都在事务外。

const reviewSchedulerInterval = 10 * time.Second

// 每 tick 批量上限：防抖消费 50、其余阶段 20、派发认领 4。
const (
	reviewDebounceBatch = 50
	reviewStageBatch    = 20
	reviewDispatchBatch = 4
)

// Start 启动审查调度循环（幂等：重复调用不叠加第二个循环）。
func (s *ReviewService) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	cancel := make(chan struct{})
	kickCh := make(chan struct{}, 1)
	s.cancel = cancel
	s.kickCh = kickCh
	s.mu.Unlock()
	go s.reviewLoop(cancel, kickCh)
}

// Stop 停止调度循环；幂等，未启动时为空操作。
func (s *ReviewService) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.kickCh = nil
	s.mu.Unlock()
	if cancel != nil {
		close(cancel)
	}
}

func (s *ReviewService) reviewLoop(cancel <-chan struct{}, kickCh <-chan struct{}) {
	ticker := time.NewTicker(reviewSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.reviewTick(now.UTC())
		case <-kickCh:
			s.reviewTick(time.Now().UTC())
		}
	}
}

// reviewTick 每轮顺序执行八个阶段；各阶段内部逐条容错，互不打断。
// 归档在工作区清理之前：删除必须以归档完成为前提。
func (s *ReviewService) reviewTick(now time.Time) {
	s.processDueDebounces(now)
	s.promoteQueuedRuns(now)
	s.dispatchRuns(now)
	s.markTimeouts(now)
	s.interruptCancelledRuns(now)
	s.syncPendingPRs(now)
	s.archiveRuns(now)
	s.cleanupWorktrees(now)
	s.cleanupLegacyThreads(now)
}

// cleanupLegacyThreads 处理通用自动化下线时留下的在途线程墓碑：停线程、
// 归档，成功即删行；失败有限重试后放弃（只留日志），避免墓碑无限累积。
func (s *ReviewService) cleanupLegacyThreads(now time.Time) {
	if s.t3 == nil {
		return
	}
	var rows []model.LegacyAutomationThread
	if err := s.db.Order("id ASC").Limit(reviewStageBatch).Find(&rows).Error; err != nil {
		s.logger.Error("查询旧自动化线程墓碑失败", zap.Error(err))
		return
	}
	for _, row := range rows {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, stopErr := s.t3.StopThread(ctx, row.UserID, row.EnvironmentID, row.ThreadID)
		var archiveErr error
		if stopErr == nil {
			archiveErr = s.t3.ArchiveThread(ctx, row.UserID, row.EnvironmentID, row.ThreadID)
		}
		cancel()
		if stopErr != nil || archiveErr != nil {
			if row.Attempts+1 >= reviewWorktreeMaxTries {
				s.logger.Warn("旧自动化线程清理失败，放弃墓碑",
					zap.String("thread", row.ThreadID), zap.Error(stopErr), zap.Error(archiveErr))
				s.db.Where("id = ?", row.ID).Delete(&model.LegacyAutomationThread{})
				continue
			}
			s.db.Model(&model.LegacyAutomationThread{}).Where("id = ?", row.ID).
				Update("attempts", row.Attempts+1)
			continue
		}
		s.db.Where("id = ?", row.ID).Delete(&model.LegacyAutomationThread{})
	}
}

// processDueDebounces 到期防抖行逐行短事务消费：条件删除（fire_at 未推后、
// head 未更新）→ 复核项目仍启用/动作仍勾选/仓库归属未变 → openRoundTx。
// 顺带清理超龄 delivery 幂等账。
func (s *ReviewService) processDueDebounces(now time.Time) {
	var rows []model.ReviewDebounce
	if err := s.db.Where("fire_at <= ?", now).Order("fire_at ASC").Limit(reviewDebounceBatch).
		Find(&rows).Error; err != nil {
		s.logger.Error("查询到期防抖行失败", zap.Error(err))
		return
	}
	for i := range rows {
		s.consumeDebounce(&rows[i], now)
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		_, err := s.deliveries.DeleteOlderThanTx(tx, now.Add(-reviewDeliveryRetention))
		return err
	})
}

// consumeDebounce 一行防抖的消费事务：条件删除防重入，复核后同事务开轮。
func (s *ReviewService) consumeDebounce(row *model.ReviewDebounce, now time.Time) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where(
			"project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND fire_at <= ?",
			row.ProjectID, row.RepoFullName, row.PRNumber, row.HeadSHA, now,
		).Delete(&model.ReviewDebounce{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // 已被更新（新 head 覆盖）或已消费
		}
		var project model.Project
		if err := tx.Where("id = ?", row.ProjectID).First(&project).Error; err != nil {
			return err
		}
		var settings model.ProjectReviewSettings
		if err := tx.Where("project_id = ?", row.ProjectID).First(&settings).Error; err != nil {
			return err
		}
		// 复核：仍启用、动作仍勾选、仓库（代码仓库，未单独配置即公开仓库）
		// 仍归属该项目。
		if !settings.Enabled || !reviewTriggerEnabled(&settings, row.Action) {
			return nil
		}
		owner, _ := splitRepoName(row.RepoFullName)
		if owner == "" || !equalFoldStr(project.CodeRepoFullName(), row.RepoFullName) {
			return nil
		}
		_, err := s.openRoundTx(tx, &project, &settings,
			row.RepoFullName, row.PRNumber, row.HeadSHA, row.Action,
			row.PRTitle, row.PRURL, row.BaseRef, row.HeadRef, row.InstallationID, now)
		return err
	})
	if err != nil {
		s.logger.Error("消费防抖行失败",
			zap.String("repo", row.RepoFullName), zap.Int("pr", row.PRNumber), zap.Error(err))
	}
}

// promoteQueuedRuns 每用户在途 <5 时按创建时间把最老 queued 提升为
// dispatched，deadline 按提升时刻 + kind 超时重设。
func (s *ReviewService) promoteQueuedRuns(now time.Time) {
	var userIDs []string
	if err := s.db.Model(&model.ReviewRun{}).
		Where("status = ?", model.ReviewRunQueued).
		Distinct().Limit(reviewStageBatch).Pluck("user_id", &userIDs).Error; err != nil {
		s.logger.Error("查询排队用户失败", zap.Error(err))
		return
	}
	for _, userID := range userIDs {
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			return s.promoteQueuedRunsTx(tx, userID, now)
		}); err != nil {
			s.logger.Error("提升排队运行失败", zap.String("user", userID), zap.Error(err))
		}
	}
}

// reviewDispatchClaimTimeout 认领后超过这个时长仍未确认建线程（进程在
// 认领与派发之间退出/崩溃）的运行允许重新认领。窗口只覆盖从未签发过令牌
// 的运行：令牌是随机值、库中只存 SHA-256 哈希，进程重启后无法恢复原值；
// 令牌存在即证明 launchRun 至少走到过签发点，已建线程持有的旧令牌一旦
// 被新签发覆盖就再也鉴权不上——这类运行不再重试，交给 deadline 超时兜底。
const reviewDispatchClaimTimeout = 5 * time.Minute

// dispatchRuns 认领未启动的 dispatched 行并事务外 launchRun。
// 启动请求不可变重放：threadId/commandId/messageId 在运行行上预分配，
// 重试重放同一请求不会建出第二个线程；令牌只在运行从未签发过时才新签。
func (s *ReviewService) dispatchRuns(now time.Time) {
	reclaimBefore := now.Add(-reviewDispatchClaimTimeout)
	// 认领条件：dispatched 且未认领，或认领超窗仍未确认建线程，且从未签发
	// 令牌（NOT EXISTS）。令牌存在的不重认领——见上面常量注释。
	const pending = `status = ?
		AND (dispatch_started_at IS NULL OR (dispatched_at IS NULL AND dispatch_started_at < ?))
		AND NOT EXISTS (SELECT 1 FROM review_run_tokens t WHERE t.run_id = review_runs.id)`
	var runs []model.ReviewRun
	if err := s.db.
		Where(pending, model.ReviewRunDispatched, reclaimBefore).
		Order("created_at ASC").Limit(reviewDispatchBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待派发运行失败", zap.Error(err))
		return
	}
	for _, run := range runs {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND "+pending, run.ID, model.ReviewRunDispatched, reclaimBefore).
			Updates(map[string]any{"dispatch_started_at": now, "updated_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		s.launchRun(context.Background(), run.ID, now)
	}
}

// markTimeouts dispatched/running 过 deadline → timed_out + 消费令牌 +
// 置 PR sync_pending + 通知。
func (s *ReviewService) markTimeouts(now time.Time) {
	var runs []model.ReviewRun
	if err := s.db.Where("status IN ? AND deadline_at <= ?",
		[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}, now).
		Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询超时运行失败", zap.Error(err))
		return
	}
	for i := range runs {
		run := &runs[i]
		applied := false
		err := s.db.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&model.ReviewRun{}).
				Where("id = ? AND status IN ? AND deadline_at <= ?", run.ID,
					[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}, now).
				Updates(map[string]any{
					"status":      model.ReviewRunTimedOut,
					"error_code":  "timeout",
					"finished_at": now,
					"updated_at":  now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
			applied = true
			if err := tx.Model(&model.ReviewRunToken{}).
				Where("run_id = ? AND consumed_at IS NULL", run.ID).
				Update("consumed_at", now).Error; err != nil {
				return err
			}
			return s.markPRSyncPendingByRepoTx(tx, run.ProjectID, run.RepoFullName, run.PRNumber, now)
		})
		if err != nil {
			s.logger.Error("标记审查超时失败", zap.String("run", run.ID), zap.Error(err))
			continue
		}
		if !applied {
			continue // 已被结束回报/取消抢先落终态：不发超时通知
		}
		s.publishReviewNotification(run, model.NotificationReviewRunTimeout, "运行超过截止时间仍未回报结果", now)
	}
}

// interruptCancelledRuns 被取消且已派发的运行补一次线程中断（StopThread）。
// 只发一次：先条件占 stop_attempted_at 标记再调用。
func (s *ReviewService) interruptCancelledRuns(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.Where("status = ? AND dispatch_started_at IS NOT NULL AND stop_attempted_at IS NULL",
		model.ReviewRunCancelled).
		Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待中断运行失败", zap.Error(err))
		return
	}
	for _, run := range runs {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND stop_attempted_at IS NULL", run.ID).
			Update("stop_attempted_at", now)
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		if run.ThreadID == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.t3.StopThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
		cancel()
		if err != nil {
			// 中断是尽力而为：线程可能从未建成（launch 未确认）。
			s.logger.Info("取消运行的线程中断未成功（忽略）",
				zap.String("run", run.ID), zap.Error(err))
		}
	}
}

package service

import (
	"context"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// 线程归档与工作区回收（INT-135）：
//
// 归档（archiveRuns）：终态且线程已确认（dispatched_at 非空）的运行逐个
// ArchiveThread，成功写 archived_at；失败按有限计数重试，超限置 archived_at
// 防无限重试并发通知，代码运行同时置 worktree_state=failed。
//
// 工作区删除（cleanupWorktrees）：只对「已归档完成」的代码运行执行，且
// 删除前核验行快照仍属本运行、路径未被其他运行认领。worktree_path 为空
// 不再用差集猜路径——无法证明归属时一律 failed + 通知，不删任何路径。

// reviewWorktreeMaxTries 归档等回收动作的有限重试次数（tick 数）。
const reviewWorktreeMaxTries = 3

// archiveRuns 调度阶段：挑线程「可能已建成」的终态运行逐条归档——
// dispatch_started_at 非空即可能已发出启动，包括 dispatched_at 为空
// （请求已发出但响应丢失、随后超时）的运行：这些线程同样需要停掉/归档，
// 否则会永久游离。明确 dispatch_failed（请求帧未发出）的跳过。
func (s *ReviewService) archiveRuns(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.
		Where(`dispatch_started_at IS NOT NULL AND archived_at IS NULL AND status IN ?
			AND error_code <> 'dispatch_failed'`,
			[]model.ReviewRunStatus{model.ReviewRunCompleted, model.ReviewRunFailed,
				model.ReviewRunTimedOut, model.ReviewRunCancelled}).
		Order("finished_at ASC").Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待归档运行失败", zap.Error(err))
		return
	}
	for i := range runs {
		s.archiveRunThread(&runs[i], now)
	}
}

// archiveRunThread 单条运行的线程归档：成功写 archived_at；失败计入有限重试，
// 超限置 archived_at（不再重试）+ 通知；代码运行同时置 worktree_state=failed
// （先归档后删除是硬前提，归档最终失败就不能删工作区）。
func (s *ReviewService) archiveRunThread(run *model.ReviewRun, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 启动结果未确认（dispatched_at 为空）但可能已建成线程：先尽力停掉。
	if run.DispatchedAt == nil {
		if _, err := s.t3.StopThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
			s.logger.Info("未确认启动的运行停止线程未成功（继续尝试归档）",
				zap.String("run", run.ID), zap.Error(err))
		}
	}
	if err := s.t3.ArchiveThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err == nil {
		s.db.Model(&model.ReviewRun{}).Where("id = ? AND archived_at IS NULL", run.ID).
			Updates(map[string]any{"archived_at": now, "updated_at": now})
		s.releaseWorktreeState(run.ID)
		return
	} else {
		s.logger.Info("审查线程归档未成功（有限重试）", zap.String("run", run.ID), zap.Error(err))
		tries := s.bumpWorktreeTries(run.ID)
		if tries < reviewWorktreeMaxTries {
			return
		}
		msg := "线程归档失败: " + err.Error()
		updates := map[string]any{"archived_at": now, "updated_at": now}
		if run.Kind == model.ReviewKindCode && run.WorktreeState != model.ReviewWorktreeRemoved {
			updates["worktree_state"] = model.ReviewWorktreeFailed
			updates["worktree_error"] = msg + "，工作区未删除"
		}
		s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).Updates(updates)
		s.releaseWorktreeState(run.ID)
		s.publishReviewNotification(run, model.NotificationReviewCleanupFailed, msg, now)
	}
}

// cleanupWorktrees 调度阶段：挑终态且未清理完的代码运行逐条处理。
func (s *ReviewService) cleanupWorktrees(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.
		Where("kind = ? AND worktree_state = ? AND status IN ?",
			model.ReviewKindCode,
			model.ReviewWorktreeNone,
			[]model.ReviewRunStatus{model.ReviewRunCompleted, model.ReviewRunFailed,
				model.ReviewRunTimedOut, model.ReviewRunCancelled}).
		Order("finished_at ASC").Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待回收运行失败", zap.Error(err))
		return
	}
	for i := range runs {
		s.cleanupRunWorktree(&runs[i], now)
	}
}

// cleanupRunWorktree 单条运行的工作区回收：终态收尾 + 释放进程内状态。
// 不删的任何失败都记 worktree_state=failed + worktree_error + 通知。
func (s *ReviewService) cleanupRunWorktree(run *model.ReviewRun, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	finish := func(state model.ReviewWorktreeState, errMsg string, notify bool) {
		s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"worktree_state": state, "worktree_error": errMsg, "updated_at": now})
		s.releaseWorktreeState(run.ID)
		if notify {
			s.publishReviewNotification(run, model.NotificationReviewCleanupFailed, errMsg, now)
		}
	}

	// 从未发起派发（queued 就被取消）→ 没有线程与工作区可清。
	if run.DispatchStartedAt == nil {
		finish(model.ReviewWorktreeRemoved, "", false)
		return
	}
	// 派发在写请求帧之前失败（dispatch_failed）：线程与工作区都未建成。
	if run.DispatchedAt == nil && run.ErrorCode == "dispatch_failed" {
		finish(model.ReviewWorktreeRemoved, "", false)
		return
	}
	// 归档前置：线程可能已建成的运行要等 archiveRuns 阶段归档完成；归档最终
	// 失败已由该阶段把本行置 failed，不会走到这里。
	if run.ArchivedAt == nil {
		return
	}
	// worktree_path 为空：不再差集猜路径——无法证明归属，不删任何路径。
	path := run.WorktreePath
	if path == "" {
		finish(model.ReviewWorktreeFailed, "无法证明工作区归属：launch 窗口内未解析到本运行的工作区路径，未执行删除", true)
		return
	}
	// 删除前核验：行快照仍属本运行（repo/pr/head/thread/path），且该路径
	// 未被其他运行认领。任一不符都不得删除。
	var cur model.ReviewRun
	if err := s.db.Where("id = ?", run.ID).First(&cur).Error; err != nil {
		return // 行读取失败，下个 tick 再来
	}
	var other int64
	if err := s.db.Model(&model.ReviewRun{}).
		Where("worktree_path = ? AND id <> ?", path, run.ID).
		Count(&other).Error; err != nil {
		return // 计数失败，下个 tick 再来
	}
	if cur.RepoFullName != run.RepoFullName || cur.PRNumber != run.PRNumber ||
		cur.HeadSHA != run.HeadSHA || cur.ThreadID != run.ThreadID ||
		cur.WorktreePath != path || other > 0 {
		finish(model.ReviewWorktreeFailed, "工作区归属核验未通过，未执行删除", true)
		return
	}
	// 删除 worktree：cwd 优先用 launch 时快照的 workspace_root——绑定之后被
	// 删/改时，当前绑定不能代表当时的工作区根目录。
	cwd := run.WorkspaceRoot
	if cwd == "" {
		binding := s.bindingFor(run)
		if binding == nil {
			finish(model.ReviewWorktreeFailed, "环境绑定缺失，无法定位工作区", true)
			return
		}
		cwd = binding.WorkspaceRoot
		if cwd == "" {
			cwd = binding.T3ProjectID
		}
	}
	if err := s.t3.RemoveWorktree(ctx, run.UserID, run.EnvironmentID, cwd, path, true); err != nil {
		finish(model.ReviewWorktreeFailed, "删除工作区失败: "+err.Error(), true)
		return
	}
	finish(model.ReviewWorktreeRemoved, "", false)
}

// bindingFor 读运行组合的环境→项目绑定（t3_project_id 真值）。
func (s *ReviewService) bindingFor(run *model.ReviewRun) *model.ProjectT3Binding {
	var binding model.ProjectT3Binding
	if err := s.db.Where("project_id = ? AND environment_id = ?", run.ProjectID, run.EnvironmentID).
		First(&binding).Error; err != nil {
		return nil
	}
	return &binding
}

func (s *ReviewService) bumpWorktreeTries(runID string) int {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	s.worktreeTries[runID]++
	return s.worktreeTries[runID]
}

// CleanupBeforeProjectDelete 删除项目前尽力清理远端资源：项目删除会 CASCADE
// 掉审查运行行，调度之后再也发现不了这些线程/工作区。这里只做 best-effort
// （失败只记日志，不阻断删除），与正常回收的「先归档后删除」顺序一致。
func (s *ReviewService) CleanupBeforeProjectDelete(ctx context.Context, projectID string) {
	if s.t3 == nil {
		return
	}
	// 与 launchRun 互斥：等在途启动结束，再把非终态运行一律取消，避免
	// 清理之后又有新线程被建出来（运行行随后会被 CASCADE 删掉）。
	unlock := s.lockProject(projectID)
	defer unlock()
	now := time.Now().UTC()
	active := []model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}
	var activeIDs []string
	if err := s.db.Model(&model.ReviewRun{}).
		Where("project_id = ? AND status IN ?", projectID, active).
		Pluck("id", &activeIDs).Error; err != nil {
		s.logger.Warn("删除项目前查询在途审查失败", zap.String("project", projectID), zap.Error(err))
	}
	if len(activeIDs) > 0 {
		if err := s.db.Model(&model.ReviewRun{}).
			Where("id IN ? AND status IN ?", activeIDs, active).
			Updates(map[string]any{
				"status": model.ReviewRunCancelled, "error_code": "project_deleted",
				"finished_at": now, "updated_at": now,
			}).Error; err != nil {
			s.logger.Warn("删除项目前取消在途审查失败", zap.String("project", projectID), zap.Error(err))
		}
		if err := s.db.Model(&model.ReviewRunToken{}).
			Where("run_id IN ? AND consumed_at IS NULL", activeIDs).
			Update("consumed_at", now).Error; err != nil {
			s.logger.Warn("删除项目前消费审查令牌失败", zap.String("project", projectID), zap.Error(err))
		}
	}
	var runs []model.ReviewRun
	if err := s.db.Where("project_id = ?", projectID).Find(&runs).Error; err != nil {
		s.logger.Warn("删除项目前查询审查运行失败", zap.String("project", projectID), zap.Error(err))
		return
	}
	for i := range runs {
		run := &runs[i]
		if run.DispatchedAt == nil || run.ThreadID == "" {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// 无条件尝试停止：终态线程的 StopThread 是幂等空操作，而刚刚被本函数
		// 取消的在途运行必须真的停掉。
		if _, err := s.t3.StopThread(callCtx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
			s.logger.Warn("删除项目前停止审查线程失败", zap.String("run", run.ID), zap.Error(err))
		}
		if err := s.t3.ArchiveThread(callCtx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
			s.logger.Warn("删除项目前归档审查线程失败", zap.String("run", run.ID), zap.Error(err))
		}
		if run.Kind == model.ReviewKindCode && run.WorktreePath != "" {
			cwd := run.WorkspaceRoot
			if cwd == "" {
				if binding := s.bindingFor(run); binding != nil {
					cwd = binding.WorkspaceRoot
					if cwd == "" {
						cwd = binding.T3ProjectID
					}
				}
			}
			if cwd != "" {
				if err := s.t3.RemoveWorktree(callCtx, run.UserID, run.EnvironmentID, cwd, run.WorktreePath, true); err != nil {
					s.logger.Warn("删除项目前回收审查工作区失败", zap.String("run", run.ID), zap.Error(err))
				}
			}
		}
		cancel()
	}
	// 防抖行没有远端资源，直接清掉（防抖行 FK 到项目，删除时会一起走，这里
	// 只是避免删除前的窗口里被开轮）。
	if err := s.db.Where("project_id = ?", projectID).Delete(&model.ReviewDebounce{}).Error; err != nil {
		s.logger.Warn("删除项目前清理防抖行失败", zap.String("project", projectID), zap.Error(err))
	}
}

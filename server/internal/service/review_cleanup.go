package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// 工作区删除（cleanupWorktrees）：只对「已归档完成」的代码运行执行。工作区
// 真值只认线程投影的 worktreePath/branch（launch 窗口差集推断已废弃），
// 删除前再经归属核验（verifyWorktreeOwnership：行快照 + 认领 + listRefs 登记
// + ref 名一致性）；任何不确定都只重试或记失败，绝不删未经证明的路径。

// reviewWorktreeMaxTries 归档等回收动作的有限重试次数（tick 数）。
const reviewWorktreeMaxTries = 3

// 回收阶段的有限重试按原因独立计数（archive/线程读取/空路径/404/登记）：
// 混用同一个计数会让混合失败序列里某个原因的实际宽限缩水。
const (
	worktreeTryArchive   = "archive"
	worktreeTryThread    = "thread-read"
	worktreeTryEmptyPath = "empty-path"
	worktreeTryNotFound  = "thread-404"
	worktreeTryRegistry  = "registry"
)

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
		s.releaseWorktreeTries(run.ID)
		return
	} else {
		s.logger.Info("审查线程归档未成功（有限重试）", zap.String("run", run.ID), zap.Error(err))
		tries := s.bumpWorktreeTries(run.ID, worktreeTryArchive)
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
		s.releaseWorktreeTries(run.ID)
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
// 路径真值来自线程投影（worktreePath/branch）；行里可能还是空的——launch
// 阶段不再解析路径。删除前经 verifyWorktreeOwnership 核验归属；不删的任何
// 失败都记 worktree_state=failed + worktree_error + 通知；读真值/登记的失败
// 走按原因独立计数的有限重试，重试中的分支不调 finish（否则计数被释放）。
func (s *ReviewService) cleanupRunWorktree(run *model.ReviewRun, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	finish := func(state model.ReviewWorktreeState, errMsg string, notify bool) {
		s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"worktree_state": state, "worktree_error": errMsg, "updated_at": now})
		s.releaseWorktreeTries(run.ID)
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

	// 线程投影是工作区归属的唯一真值；读不到真值（T3 建 worktree 滞后于线程
	// 创建、上游暂时性失败）都按有限重试处理。
	threadWT, err := s.t3.ThreadWorktree(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			// 线程 404 不立即采信：可能来自 T3 重启/代理抖动，同样走有限重试，
			// 连续到上限才认定线程确已不存在。取舍：线程被删除后其磁盘工作区
			// 归 T3 生命周期管，Fast Ship 不再猜测路径，所以终态是 removed 而
			// 不是 failed——不删不通知，只等确认。
			if tries := s.bumpWorktreeTries(run.ID, worktreeTryNotFound); tries < reviewWorktreeMaxTries {
				return
			}
			finish(model.ReviewWorktreeRemoved, "", false)
			return
		}
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryThread); tries < reviewWorktreeMaxTries {
			return
		}
		finish(model.ReviewWorktreeFailed, "读取线程工作区失败: "+err.Error(), true)
		return
	}
	path, branch := "", ""
	if threadWT != nil {
		path, branch = threadWT.Path, threadWT.Branch
	}
	if path == "" {
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryEmptyPath); tries < reviewWorktreeMaxTries {
			return
		}
		// 线程确实没有 worktree（如视觉式 root 工作区）：没有可清的。
		finish(model.ReviewWorktreeRemoved, "", false)
		return
	}
	// 行内旧快照与线程真值冲突：可能是行被并发改写或线程被复用，不删。
	if run.WorktreePath != "" && run.WorktreePath != path {
		finish(model.ReviewWorktreeFailed, "运行记录的工作区与线程记录不一致，未执行删除", true)
		return
	}
	// 把线程真值写回运行行（只在仍为空时补，不覆盖其他写入）。
	if run.WorktreePath == "" {
		s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND worktree_path = ''", run.ID).
			Updates(map[string]any{"worktree_path": path, "updated_at": now})
	}

	// 核验与删除用同一个 cwd：launch 时快照的 workspace_root 优先——绑定之后
	// 被删/改时，当前绑定不能代表当时的工作区根目录。
	cwd := s.worktreeCwdFor(run)
	if cwd == "" {
		finish(model.ReviewWorktreeFailed, "环境绑定缺失，无法定位工作区", true)
		return
	}
	verdict, reason, err := s.verifyWorktreeOwnership(ctx, run, path, branch, cwd)
	if err != nil {
		// 暂时读不到（运行行/认领/登记）：有限重试，超限记失败。
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryRegistry); tries < reviewWorktreeMaxTries {
			return
		}
		finish(model.ReviewWorktreeFailed, err.Error(), true)
		return
	}
	switch verdict {
	case worktreeGone:
		// 已不在登记中（被用户或其他途径删掉）：没有可删的。
		finish(model.ReviewWorktreeRemoved, "", false)
	case worktreeMismatch:
		finish(model.ReviewWorktreeFailed, reason, true)
	case worktreeOwned:
		if err := s.t3.RemoveWorktree(ctx, run.UserID, run.EnvironmentID, cwd, path, true); err != nil {
			finish(model.ReviewWorktreeFailed, "删除工作区失败: "+err.Error(), true)
			return
		}
		finish(model.ReviewWorktreeRemoved, "", false)
	}
}

// worktreeVerdict 是归属核验的结论。
type worktreeVerdict int

const (
	worktreeOwned    worktreeVerdict = iota // 归属已证明，可删除
	worktreeGone                            // 登记里已没有该路径：没有可删的
	worktreeMismatch                        // 快照/认领/分支冲突：不得删除
)

// verifyWorktreeOwnership 证明 path 仍属本运行，常规回收与项目删除共用同一份
// 实现（项目删除入口不得绕过核验强删可能已被另作他用的路径）。核验顺序：
// 运行行快照（repo/pr/head/thread/path）→ 其他运行认领 → listRefs 登记 →
// ref 名与线程分支一致。error 非空表示暂时读不到（可重试），verdict 无效；
// mismatch 时 reason 给出人读得懂的原因。
func (s *ReviewService) verifyWorktreeOwnership(ctx context.Context, run *model.ReviewRun, path, branch, cwd string) (worktreeVerdict, string, error) {
	// 行快照：运行行可能被并发改写，以库内现值为准重核一遍。
	var cur model.ReviewRun
	if err := s.db.Where("id = ?", run.ID).First(&cur).Error; err != nil {
		return 0, "", fmt.Errorf("读取运行行失败: %w", err)
	}
	if cur.RepoFullName != run.RepoFullName || cur.PRNumber != run.PRNumber ||
		cur.HeadSHA != run.HeadSHA || cur.ThreadID != run.ThreadID ||
		cur.WorktreePath != path {
		return worktreeMismatch, "工作区归属核验未通过，未执行删除", nil
	}
	// 认领核验：只认仍在回收流程里的行（worktree_state=none）。removed/failed
	// 是历史终态，路径只是残留快照，把它们算认领者会让后续同路径的合法属主
	// 永久 failed。
	var other int64
	if err := s.db.Model(&model.ReviewRun{}).
		Where("worktree_path = ? AND id <> ? AND worktree_state = ?", path, run.ID, model.ReviewWorktreeNone).
		Count(&other).Error; err != nil {
		return 0, "", fmt.Errorf("统计工作区认领失败: %w", err)
	}
	if other > 0 {
		return worktreeMismatch, "工作区归属核验未通过，未执行删除", nil
	}
	// 登记核验：该 worktree 必须仍登记在 cwd 之下——防误删已被另作他用的
	// 同路径工作区。
	registered, err := s.t3.ListWorktrees(ctx, run.UserID, run.EnvironmentID, cwd)
	if err != nil {
		return 0, "", fmt.Errorf("读取项目工作区登记失败: %w", err)
	}
	idx := slices.IndexFunc(registered, func(w T3Worktree) bool { return w.Path == path })
	if idx < 0 {
		return worktreeGone, "", nil
	}
	// 分支核验：线程投影的 branch 可空（F4 前缀的误判已修），非空时才要求
	// 登记的 ref 名集合包含它；为空时只凭路径命中即认定归属。
	if branch != "" && !slices.Contains(registered[idx].RefNames, branch) {
		return worktreeMismatch, "工作区分支与线程记录不一致，未执行删除", nil
	}
	return worktreeOwned, "", nil
}

// worktreeCwdFor 回收链路的 cwd：launch 时快照的 workspace_root 优先，为空
// 回退当前绑定（workspace_root → t3_project_id）。核验与删除都必须用它，
// 保证对着同一个工作区根目录读登记与执行删除。
func (s *ReviewService) worktreeCwdFor(run *model.ReviewRun) string {
	if run.WorkspaceRoot != "" {
		return run.WorkspaceRoot
	}
	binding := s.bindingFor(run)
	if binding == nil {
		return ""
	}
	if binding.WorkspaceRoot != "" {
		return binding.WorkspaceRoot
	}
	return binding.T3ProjectID
}

// bumpWorktreeTries 按原因递增一个运行的回收重试计数，返回当前值。key 用
// runID+reason：不同失败原因各自拿满宽限，不互相挤占。
func (s *ReviewService) bumpWorktreeTries(runID, reason string) int {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	key := runID + "|" + reason
	s.worktreeTries[key]++
	return s.worktreeTries[key]
}

// releaseWorktreeTries 释放一个运行的回收重试计数：归档/回收走到终态时调用，
// 防泄漏。重试中的分支不得调用（否则下个 tick 重新计，上限形同虚设）。
func (s *ReviewService) releaseWorktreeTries(runID string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	prefix := runID + "|"
	for key := range s.worktreeTries {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(s.worktreeTries, key)
		}
	}
}

// CleanupBeforeProjectDelete 删除项目前尽力清理远端资源：项目删除会 CASCADE
// 掉审查运行行，调度之后再也发现不了这些线程/工作区。这里只做 best-effort
// （失败只记日志，不阻断删除），与正常回收共用同一份归属核验——核验不过
// （gone/mismatch）只记日志跳过，绝不因为「项目要删了」就强删未证明的路径。
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
		if run.ThreadID == "" {
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
		s.cleanupWorktreeBeforeDelete(callCtx, run, now)
		cancel()
	}
	// 防抖行没有远端资源，直接清掉（防抖行 FK 到项目，删除时会一起走，这里
	// 只是避免删除前的窗口里被开轮）。
	if err := s.db.Where("project_id = ?", projectID).Delete(&model.ReviewDebounce{}).Error; err != nil {
		s.logger.Warn("删除项目前清理防抖行失败", zap.String("project", projectID), zap.Error(err))
	}
}

// cleanupWorktreeBeforeDelete 项目删除入口的单条工作区回收：行内 worktree_path
// 优先；为空按线程投影取真值；线程读取失败就保留行内路径且分支置空（核验
// 退化为只查路径登记）。之后走与常规回收相同的 verifyWorktreeOwnership，
// owned 才删除，gone/mismatch 只记日志——本函数没有重试机会（运行行马上
// 随项目 CASCADE），但核验宁缺毋滥。
func (s *ReviewService) cleanupWorktreeBeforeDelete(ctx context.Context, run *model.ReviewRun, now time.Time) {
	if run.Kind != model.ReviewKindCode {
		return
	}
	path, branch := run.WorktreePath, ""
	if wt, err := s.t3.ThreadWorktree(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
		s.logger.Warn("删除项目前读取审查线程工作区失败", zap.String("run", run.ID), zap.Error(err))
	} else if wt != nil && (path == "" || wt.Path == path) {
		// 线程路径与所用路径一致才采信其分支，避免拿另一条工作区的分支核验。
		if path == "" {
			path = wt.Path
		}
		branch = wt.Branch
	}
	if path == "" {
		return
	}
	// 与常规回收同口径：线程真值写回运行行（仅空时补），核验的行快照才有据可比。
	if run.WorktreePath == "" {
		s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND worktree_path = ''", run.ID).
			Updates(map[string]any{"worktree_path": path, "updated_at": now})
		run.WorktreePath = path
	}
	cwd := s.worktreeCwdFor(run)
	if cwd == "" {
		s.logger.Warn("删除项目前无法定位工作区根目录，跳过回收", zap.String("run", run.ID))
		return
	}
	verdict, reason, err := s.verifyWorktreeOwnership(ctx, run, path, branch, cwd)
	if err != nil {
		s.logger.Warn("删除项目前核验工作区归属失败，跳过回收",
			zap.String("run", run.ID), zap.Error(err))
		return
	}
	switch verdict {
	case worktreeGone:
		s.logger.Info("删除项目前工作区已不在登记中，跳过删除", zap.String("run", run.ID), zap.String("path", path))
	case worktreeMismatch:
		s.logger.Warn("删除项目前工作区归属核验未通过，跳过删除",
			zap.String("run", run.ID), zap.String("path", path), zap.String("reason", reason))
	case worktreeOwned:
		if err := s.t3.RemoveWorktree(ctx, run.UserID, run.EnvironmentID, cwd, path, true); err != nil {
			s.logger.Warn("删除项目前回收审查工作区失败", zap.String("run", run.ID), zap.Error(err))
		}
	}
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

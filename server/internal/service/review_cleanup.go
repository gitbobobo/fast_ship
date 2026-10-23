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
// 防无限重试并发通知，代码运行同时置 worktree_state=failed。归档前有收尾
// 宽限（INT-158）：终态落库后 agent 的 T3 run 可能还在写收尾回复，宽限窗口
// 内等它收尾再归档，避免切断 provider 事件流产生僵尸线程。
//
// 工作区删除（cleanupWorktrees）：只对「已归档完成」的代码运行执行。工作区
// 真值只认线程投影的 worktreePath/branch（launch 窗口差集推断已废弃），
// 删除前再经归属核验（verifyWorktreeOwnership：行快照 + 认领 + listRefs 登记
// + ref 名一致性）；任何不确定都只重试或记失败，绝不删未经证明的路径。

// reviewWorktreeMaxTries 归档等回收动作的有限重试次数（tick 数）。
const reviewWorktreeMaxTries = 3

// reviewArchiveSettleWait 归档前的收尾宽限（INT-158）：结果落库后 agent 的
// T3 run 往往还在写收尾回复，立刻归档切断 provider 事件流，T3 把 run 误记
// 为 failed（status=archived / settled=false 的僵尸线程）。宽限窗口内先读
// 线程投影等 run 收尾；等待不占归档有限重试计数，宽限即上限，超窗直接归档
// 兜底。
const reviewArchiveSettleWait = 10 * time.Minute

// reviewRunNeverLaunched 「确定没有建成线程」的谓词。pr_closed 一定是未建
// 线程：该取消发生在 launchRun 的 PR 实况关卡（CreateThreadForAgent 之前），
// 运行落 cancelled 时启动请求从未发出；dispatch_failed 是请求帧未写出，
// 线程同样未建。archiveRuns 与 cleanupRunWorktree 用同一份判定，避免一处
// 漏改把「无线程可停/可归档」误判成「回收失败」发假告警。不要泛化到所有
// dispatched_at 为空的运行——结果未确认（请求已发出）的运行线程可能实际
// 已建成，仍要尝试停掉/归档，否则线程永久游离。
func reviewRunNeverLaunched(run *model.ReviewRun) bool {
	return run.DispatchedAt == nil &&
		(run.ErrorCode == "dispatch_failed" || run.ErrorCode == "pr_closed")
}

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
// 否则会永久游离。确定无线程的运行跳过（reviewRunNeverLaunched：请求帧未
// 发出的 dispatch_failed、建线程前就被 PR 关闭关卡拦下的 pr_closed）；SQL
// 条件与该谓词同口径，谓词改动须同步此处。
func (s *ReviewService) archiveRuns(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.
		Where(`dispatch_started_at IS NOT NULL AND archived_at IS NULL AND status IN ?
			AND (dispatched_at IS NOT NULL OR
				(COALESCE(error_code, '') <> 'dispatch_failed' AND COALESCE(error_code, '') <> 'pr_closed'))`,
			[]model.ReviewRunStatus{model.ReviewRunCompleted, model.ReviewRunFailed,
				model.ReviewRunTimedOut, model.ReviewRunCancelled}).
		Order("finished_at ASC").Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待归档运行失败", zap.Error(err))
		return
	}
	start := time.Now()
	for i := range runs {
		if time.Since(start) > reviewNetworkStageBudget {
			s.logger.Info("归档到达阶段网络预算，剩余行下轮处理", zap.Int("remaining", len(runs)-i))
			break
		}
		s.archiveRunThread(&runs[i], now)
	}
}

// archiveRunThread 单条运行的线程归档：先按收尾宽限等绑定 T3 run 收尾
// （INT-158），再 ArchiveThread；归档成功写 archived_at；失败计入有限重试，
// 超限置 archived_at（不再重试）+ 通知；代码运行同时置 worktree_state=failed
// （先归档后删除是硬前提，归档最终失败就不能删工作区）。
func (s *ReviewService) archiveRunThread(run *model.ReviewRun, now time.Time) {
	// 单行预算与阶段预算同量级（60s）：归档动作整体不能拖住后续阶段；各步
	// 内部还有 t3WriteBudget 收紧。前序步骤把行预算耗尽时归档会返回 ctx
	// 截止错误——那是「暂时读不到」而非归档失败，按等待处理不占重试计数，
	// 下轮重试（宽限/行预算兜底见下）。
	ctx, cancel := context.WithTimeout(context.Background(), reviewNetworkStageBudget)
	defer cancel()

	// 启动结果未确认（dispatched_at 为空）但可能已建成线程：先尽力停掉
	// （仍在归档前；宽限等待期间每个 tick 都会再发一次，幂等，帮孤儿回合
	// 尽快终止）。
	if run.DispatchedAt == nil {
		if _, err := s.t3.StopThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err != nil {
			s.logger.Info("未确认启动的运行停止线程未成功（继续尝试归档）",
				zap.String("run", run.ID), zap.Error(err))
		}
	}
	// 收尾宽限：仅 finished_at 之后的宽限窗口内等待（finished_at 为空或已超
	// 宽限都跳过等待直接归档，防御 + 兜底）。等待不占归档有限重试计数。
	if run.FinishedAt != nil && now.Sub(*run.FinishedAt) < reviewArchiveSettleWait {
		settled, err := s.t3.ThreadTurnSettled(ctx, run.UserID, run.EnvironmentID, run.ThreadID, run.InitialMessageID)
		switch {
		case err == nil:
			// 读到了线程投影：之前的连续 404 不再算「连续」。
			s.resetWorktreeTries(run.ID, worktreeTryNotFound)
			if !settled {
				// run 还在写收尾回复：本 tick 不归档，等下轮。
				return
			}
		case errors.Is(err, errT3ThreadNotFound):
			// 线程 404 不立即采信（T3 重启/代理抖动，与 cleanupRunWorktree
			// 同口径）：连续到上限才认定无可归对象。
			if s.confirmThreadGone(run) {
				s.archiveConfirmedGone(run, now)
			}
			return
		default:
			// 暂时读不到收尾状态：本 tick 不归档，下轮再试（宽限即上限）。
			s.logger.Info("归档前读取线程收尾状态未成功，等下轮再试",
				zap.String("run", run.ID), zap.Error(err))
			return
		}
		// settled：落入归档。
	}
	if err := s.t3.ArchiveThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID); err == nil {
		s.db.Model(&model.ReviewRun{}).Where("id = ? AND archived_at IS NULL", run.ID).
			Updates(map[string]any{"archived_at": now, "updated_at": now})
		s.releaseWorktreeTries(run.ID)
		return
	} else if ctx.Err() != nil {
		// 行预算被停线程/投影读耗尽：归档是被掐断而不是被拒绝，本 tick 跳过
		// 不占重试，下轮再试——与收尾读失败的等待语义一致。
		s.logger.Info("归档行预算被前序步骤耗尽，等下轮再试",
			zap.String("run", run.ID), zap.Error(err))
		return
	} else {
		// 归档失败的归因可能是「线程已经没了」：超宽限/finished_at 为空的
		// 行不读投影直接归档，用户删线程、T3 侧清理、停机后补跑都会让
		// dispatchCommand 返回普通上游错误（不带 errT3ThreadNotFound）。补读
		// 一次投影归因：404 走同一份有界确认，证实不存在直接落账——否则
		// 无线程的归档失败会被误记成归档错误，到顶发假告警还置 failed。
		_, perr := s.t3.ThreadTurnSettled(ctx, run.UserID, run.EnvironmentID, run.ThreadID, run.InitialMessageID)
		if errors.Is(perr, errT3ThreadNotFound) {
			if s.confirmThreadGone(run) {
				s.archiveConfirmedGone(run, now)
			}
			return
		}
		if perr == nil {
			// 读到了线程投影：之前的连续 404 不再算「连续」。
			s.resetWorktreeTries(run.ID, worktreeTryNotFound)
		}
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

// confirmThreadGone 累加一次连续 404 并报告是否已到上限：单次 404 不采信
// （T3 重启/代理抖动，与 cleanupRunWorktree 同口径），连续到上限才认定
// 线程确已不存在。
func (s *ReviewService) confirmThreadGone(run *model.ReviewRun) bool {
	return s.bumpWorktreeTries(run.ID, worktreeTryNotFound) >= reviewWorktreeMaxTries
}

// archiveConfirmedGone 线程已被证实不存在（连续 404 到上限）：无可归对象，
// 直接落 archived_at 并释放重试计数——不走归档 RPC（无线程的 RPC 只会误记
// 归档失败、发假告警），不置 worktree_state=failed，也不发通知。
func (s *ReviewService) archiveConfirmedGone(run *model.ReviewRun, now time.Time) {
	s.db.Model(&model.ReviewRun{}).Where("id = ? AND archived_at IS NULL", run.ID).
		Updates(map[string]any{"archived_at": now, "updated_at": now})
	s.releaseWorktreeTries(run.ID)
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
	// 确定没有建成线程的（dispatch_failed 请求帧未发出、pr_closed 建线程前
	// 就被拦下）：线程与工作区都未建成，无可停、无可归、无可删。
	if reviewRunNeverLaunched(run) {
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
	// 读到了线程投影：之前的读取/404 失败不再算「连续」——重试宽限按连续
	// 失败计，中途成功必须清零，否则瞬时抖动会与旧失败叠加提前收尾。
	s.resetWorktreeTries(run.ID, worktreeTryThread)
	s.resetWorktreeTries(run.ID, worktreeTryNotFound)
	if path != "" {
		s.resetWorktreeTries(run.ID, worktreeTryEmptyPath)
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
	// 把线程真值写回运行行（只在仍为空时补，不覆盖其他写入）。写失败就等
	// 下个 tick 重试：行里仍是空路径时核验的行快照必然不符，会把一次瞬时
	// DB 失败误报成「归属核验未通过」。
	if run.WorktreePath == "" {
		if err := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND worktree_path = ''", run.ID).
			Updates(map[string]any{"worktree_path": path, "updated_at": now}).Error; err != nil {
			return
		}
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
// 回退当前绑定。核验与删除都必须用它，保证对着同一个工作区根目录读登记与
// 执行删除。绑定的 workspace_root 在写入时已校验非空（t3_binding.go），拿
// 不到绑定就返回空串，由调用方按失败/跳过处理。
func (s *ReviewService) worktreeCwdFor(run *model.ReviewRun) string {
	if run.WorkspaceRoot != "" {
		return run.WorkspaceRoot
	}
	if binding := s.bindingFor(run); binding != nil {
		return binding.WorkspaceRoot
	}
	return ""
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

// resetWorktreeTries 清零一个运行某个原因的重试计数：该原因本轮成功后，
// 「连续失败 N 次」的语义才算数（否则累计失败会在中途成功后又直接收尾）。
func (s *ReviewService) resetWorktreeTries(runID, reason string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	delete(s.worktreeTries, runID+"|"+reason)
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

// cleanupWorktreeBeforeDelete 项目删除入口的单条工作区回收：以线程投影真值
// 为唯一依据（行内 worktree_path 只用来核对线程路径是否一致）；读不到投影、
// 投影没有路径、或与行内路径冲突都直接跳过删除——项目删除没有重试机会，
// 宁缺毋滥，绝不允许用未知/冲突的归属信息降级成「路径 + 空分支」强删。
// 核验与常规回收共用 verifyWorktreeOwnership，owned 才删除。
func (s *ReviewService) cleanupWorktreeBeforeDelete(ctx context.Context, run *model.ReviewRun, now time.Time) {
	if run.Kind != model.ReviewKindCode {
		return
	}
	// 运行行马上会被 CASCADE：进程内的回收重试计数已无意义，顺手释放。
	defer s.releaseWorktreeTries(run.ID)
	wt, err := s.t3.ThreadWorktree(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
	if err != nil {
		s.logger.Warn("删除项目前读取审查线程工作区失败，跳过回收", zap.String("run", run.ID), zap.Error(err))
		return
	}
	if wt == nil || wt.Path == "" {
		s.logger.Info("删除项目前线程没有工作区真值，跳过回收", zap.String("run", run.ID))
		return
	}
	if run.WorktreePath != "" && run.WorktreePath != wt.Path {
		s.logger.Warn("删除项目前运行记录的工作区与线程记录不一致，跳过回收",
			zap.String("run", run.ID), zap.String("row", run.WorktreePath), zap.String("thread", wt.Path))
		return
	}
	path, branch := wt.Path, wt.Branch
	// 与常规回收同口径：线程真值写回运行行（仅空时补），核验的行快照才有据可比。
	if run.WorktreePath == "" {
		if err := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND worktree_path = ''", run.ID).
			Updates(map[string]any{"worktree_path": path, "updated_at": now}).Error; err != nil {
			s.logger.Warn("删除项目前写回线程工作区失败，跳过回收", zap.String("run", run.ID), zap.Error(err))
			return
		}
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

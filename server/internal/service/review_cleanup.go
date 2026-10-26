package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
// 工作区回收（cleanupWorktrees，INT-170 起两步模型）：只对「已归档完成」的
// 代码运行执行。第一步工作树：真值只认线程投影的 worktreePath（行内快照是
// 回写真值，线程 404 后是唯一依据），删除前经归属核验（verifyWorktreeOwnership：
// 行快照 + 认领 + listRefs 登记 + ref 名一致性）；第二步分支：远程 T3 没有删
// 分支的 RPC，分支删除由审查代理收尾执行（提示词「收尾」节），服务端只核验
// 登记全集里的 ref 名——分支仍在落 branch_pending 复查，消失才 removed。
// 任何不确定都只重试、退避或记失败，绝不删未经证明的路径。

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

// 回收阶段的有限重试按原因独立计数（archive/线程读取/空路径/404/登记/待
// 确认）：混用同一个计数会让混合失败序列里某个原因的实际宽限缩水。
const (
	worktreeTryArchive         = "archive"
	worktreeTryThread          = "thread-read"
	worktreeTryEmptyPath       = "empty-path"
	worktreeTryNotFound        = "thread-404"
	worktreeTryRegistry        = "registry"
	worktreeTryRegIncomplete   = "registry-incomplete"
	worktreeTryRemoveUncertain = "remove-uncertain"
	// worktreeTryRemoveAttempt 删除请求「未送达」（INT-170：环境/凭证/拨号
	// 失败，远端必然没执行）的有限重试计数——与「结果不可见」的复查退避
	// 分开计数：前者直接重试即可，后者每轮都要先复查远端实际状态。
	worktreeTryRemoveAttempt = "remove-attempt"
	// worktreeTryOwnership 归属核验的 DB 读失败（行快照/认领统计）：与登记
	// 读取分键——登记读成功每 tick 都复位 registry 键，共用会把核验错误的
	// 宽限刷成永远 1 次，持续性核验失败永远到不了上限（复审 P2）。
	worktreeTryOwnership = "ownership"
	// worktreeTryRemoveBudget 删除侧预算耗尽的连续计数（复审 P2）：必须与
	// registry-incomplete 分键——登记读成功每 tick 都会经 recoverFromPending
	// 复位后者，删除侧复用它会被清零、计数永远停在 1，既不收敛也发不出聚
	// 合。消解条件是 RemoveWorktree 重试的结果，不是登记读。
	worktreeTryRemoveBudget = "remove-budget"
	// worktreeTryBranchPending 复查确认「分支仍存活」的累计计数：到
	// reviewBranchPendingMaxTries 收敛 failed，待清理不是无限期状态（复审
	// Note）。只统计确认存活的复查——登记读失败走自己的键，不掺进来。
	worktreeTryBranchPending = "branch-pending"
)

// 回收问题分类码（INT-170，写入 review_runs.worktree_reason）：聚合通知按
// （环境, 仓库, 原因）分键查询受影响运行清单，值即对外可见的分类键——短且
// 稳定，改值等于改通知分键。
const (
	// worktreeReasonArchiveFailed 线程归档超限失败，工作区回收放弃（先归档
	// 后删除是硬前提）。
	worktreeReasonArchiveFailed = "archive_failed"
	// worktreeReasonThreadRead 线程工作区真值读取连续失败，无法证明归属。
	worktreeReasonThreadRead = "thread_read"
	// worktreeReasonRegistryError 登记读取连续失败（上游明确报错）。
	worktreeReasonRegistryError = "registry_error"
	// worktreeReasonRegistryIncomplete 登记未在时间预算内读完：不是失败，
	// 保持 none 挂退避等下一轮确认。
	worktreeReasonRegistryIncomplete = "registry_incomplete"
	// worktreeReasonOwnershipMismatch 归属核验未通过，拒绝删除。
	worktreeReasonOwnershipMismatch = "ownership_mismatch"
	// worktreeReasonWorktreeRemove 删除被远端明确拒绝（Exit Failure）。
	worktreeReasonWorktreeRemove = "worktree_remove"
	// worktreeReasonRemoveUncertain 删除请求发出但结果不可见（读超时/连接
	// 中断）：保持 none 复查远端实际状态，有界重试。
	worktreeReasonRemoveUncertain = "remove_uncertain"
	// worktreeReasonBranchPending 工作树已删、本轮专属分支仍在：服务端无删
	// 分支 RPC，等审查代理收尾或人工清理。
	worktreeReasonBranchPending = "branch_pending"
	// worktreeReasonEnvBinding 环境绑定缺失，无法定位工作区根目录。
	worktreeReasonEnvBinding = "env_binding"
	// worktreeReasonBranchUnknown 分支名缺失且登记无法恢复（升级前 NULL 行 +
	// 工作树先消失）：无法证明无专属分支残留，记失败交人工核查。
	worktreeReasonBranchUnknown = "branch_unknown"
	// worktreeReasonRemoveBudgetPending 删除执行的时间预算在行内耗尽（环
	// 境解析/拨号到点）：与 registry_incomplete 同为待确认但分键——聚合通
	// 知按原因分桶，混进「登记读取不完整」会指向错误根因（复审 P2）。
	worktreeReasonRemoveBudgetPending = "remove_budget_pending"
	// worktreeReasonOwnershipError 归属核验的 DB 读连续失败（行快照/认领统
	// 计）——本地库故障，不并入「登记读取失败」聚合键误导排查（复审 Note）。
	worktreeReasonOwnershipError = "ownership_error"
)

// reviewBudgetPendingMaxTries 待确认态连续预算耗尽的累计上限（复审 P2）：
// markBudgetPending 每 tick 记一次连续失败，退避封顶 5min 后 ≈1h 仍翻不完
// 即收敛 failed——无限期待确认只会让行既不回收也不标败、还周期性重读大登记。
const reviewBudgetPendingMaxTries = 12

// reviewBranchPendingMaxTries 分支待清理态的确认存活上限（复审 Note）：
// recheckBranchPending 每次确认分支仍存活记一次；退避封顶 1h 下 ≈1 天仍
// 未消失说明代理收尾与人工都不会再动它，收敛 failed 交人工而非永久滞留。
const reviewBranchPendingMaxTries = 24

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
// 超限置 archived_at（不再重试）+ 聚合通知（reason=archive_failed）；代码
// 运行同时置 worktree_state=failed（先归档后删除是硬前提，归档最终失败就
// 不能删工作区）。
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
		verdict, err := s.t3.ThreadTurnSettled(ctx, run.UserID, run.EnvironmentID, run.ThreadID, run.InitialMessageID)
		switch {
		case err == nil:
			// 读到了线程投影：之前的连续 404 不再算「连续」。
			s.resetWorktreeTries(run.ID, worktreeTryNotFound)
			if verdict == turnUnsettled {
				// run 还在写收尾回复：本 tick 不归档，等下轮。
				return
			}
			if verdict == turnThreadGone {
				// 线程已归档/已删除：目标态已达成，归档 RPC 非幂等只会
				// 误记归档失败——直接落账。
				s.archiveConfirmedGone(run, now)
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
		// turnSettled：落入归档。
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
		verdict, perr := s.t3.ThreadTurnSettled(ctx, run.UserID, run.EnvironmentID, run.ThreadID, run.InitialMessageID)
		if errors.Is(perr, errT3ThreadNotFound) {
			if s.confirmThreadGone(run) {
				s.archiveConfirmedGone(run, now)
			}
			return
		}
		if perr == nil {
			// 读到了线程投影：之前的连续 404 不再算「连续」。墓碑线程
			// （已归档/已删除）证明目标态已达成——归档 RPC 的失败只是
			// 非幂等报错，直接落账不计归档失败。
			s.resetWorktreeTries(run.ID, worktreeTryNotFound)
			if verdict == turnThreadGone {
				s.archiveConfirmedGone(run, now)
				return
			}
		}
		s.logger.Info("审查线程归档未成功（有限重试）", zap.String("run", run.ID), zap.Error(err))
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryArchive); tries < reviewWorktreeMaxTries {
			return
		}
		// 超限：archived_at 与回收终态必须同一笔写（codex 复审）——先落
		// archived_at 再写失败态，第二步失败会留下「已归档标记 + state=none」
		// 的半截行，下轮 cleanup 把标记当已归档删掉未归档线程的工作区。写失
		// 败则整段下轮重走（archived_at 仍空、计数未释放，重试序列不断）。
		// 已回收完（removed）的行不回填失败原因；视觉运行无工作区可清，工作
		// 区状态保持 none，只落原因供聚合清单查询。
		msg := "线程归档失败: " + err.Error() + "，工作区回收未执行"
		state, reason, notify := run.WorktreeState, "", false
		if state != model.ReviewWorktreeRemoved {
			reason, notify = worktreeReasonArchiveFailed, true
			state = model.ReviewWorktreeNone
			if run.Kind == model.ReviewKindCode {
				state = model.ReviewWorktreeFailed
			}
		}
		s.finishWorktreeCleanupExtra(run, state, reason, msg, notify, now,
			map[string]any{"archived_at": now})
	}
}

// confirmThreadGone 累加一次连续 404 并报告是否已到上限：单次 404 不采信
// （T3 重启/代理抖动，与 cleanupRunWorktree 同口径），连续到上限才认定
// 线程确已不存在。
func (s *ReviewService) confirmThreadGone(run *model.ReviewRun) bool {
	return s.bumpWorktreeTries(run.ID, worktreeTryNotFound) >= reviewWorktreeMaxTries
}

// archiveConfirmedGone 线程已被证实不存在（连续 404 到上限）或目标态已达
// 成（墓碑投影已归档/已删除）：无可归对象，直接落 archived_at 并释放重试
// 计数——不走归档 RPC（无线程的 RPC 只会误记归档失败、发假告警），不置
// worktree_state=failed，也不发通知。
func (s *ReviewService) archiveConfirmedGone(run *model.ReviewRun, now time.Time) {
	s.db.Model(&model.ReviewRun{}).Where("id = ? AND archived_at IS NULL", run.ID).
		Updates(map[string]any{"archived_at": now, "updated_at": now})
	s.releaseWorktreeTries(run.ID)
}

// cleanupWorktrees 调度阶段：挑终态且未清理完的代码运行逐条处理。none 之外
// 还包含 branch_pending（INT-170）：分支残留的行按退避复查登记，分支消失才
// 收 removed。查询不带批量上限（未完成回收行集合天然有界：终态离开、长期
// 挂退避的行被到期预判跳过）；批内按 reviewStageBatch 与阶段网络预算双上限
// 处理，先到先停。
func (s *ReviewService) cleanupWorktrees(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.
		// result_json 是结构化结论大字段，回收链路不读——全量扫描常驻
		// pending 行时按行载荷减负（codex 复审意见）。
		Omit("result_json").
		Where("kind = ? AND worktree_state IN ? AND status IN ?",
			model.ReviewKindCode,
			[]model.ReviewWorktreeState{model.ReviewWorktreeNone, model.ReviewWorktreeBranchPending},
			[]model.ReviewRunStatus{model.ReviewRunCompleted, model.ReviewRunFailed,
				model.ReviewRunTimedOut, model.ReviewRunCancelled}).
		Order("finished_at ASC").Find(&runs).Error; err != nil {
		s.logger.Error("查询待回收运行失败", zap.Error(err))
		return
	}
	start := time.Now()
	processed := 0
	for i := range runs {
		run := &runs[i]
		// 到期预判不占处理名额（INT-170）：长期挂在退避上的行未到点整个跳过
		// ——不读登记、不发网络请求，也不把名额耗在「查了也查不到变化」的行
		// 上；否则 20 条常驻 branch_pending 能把后面的行永久饿死在批量窗口外。
		// 单行函数内部的到期检查保留，防直接调用的路径绕过预判。
		if !s.worktreeReclaimDue(run, now) {
			continue
		}
		if processed >= reviewStageBatch || time.Since(start) > reviewNetworkStageBudget {
			s.logger.Info("工作区回收到达阶段上限，剩余行下轮处理",
				zap.Int("remaining", len(runs)-i))
			break
		}
		processed++
		s.cleanupRunWorktree(run, now)
	}
}

// worktreeReclaimDue 回收行到期预判（INT-170）：branch_pending 按其复查退避，
// none 行的 registry_incomplete / remove_uncertain 按各自的重试退避；其余行
// 恒到期。例外：已派发未归档行在等归档阶段——进 cleanupRunWorktree 也只立即
// 返回，但照样消耗 processed 名额，归档积压超 reviewStageBatch 时会饿死真正
// 到期的行（复审 P2）。未派发/未建线程行不在此列：无归档依赖，立即收 removed。
func (s *ReviewService) worktreeReclaimDue(run *model.ReviewRun, now time.Time) bool {
	if run.ArchivedAt == nil && run.DispatchStartedAt != nil && !reviewRunNeverLaunched(run) {
		return false
	}
	if run.WorktreeState == model.ReviewWorktreeBranchPending {
		return s.worktreeBackoffDue(run.ID, worktreeReasonBranchPending, now)
	}
	if isWorktreePendingReason(run.WorktreeReason) {
		return s.worktreeBackoffDue(run.ID, run.WorktreeReason, now)
	}
	return true
}

// isWorktreePendingReason 报告该分类码是否为「保持 none 挂退避」的持续待确
// 认原因：到期预判与行内退避闸门两处共用同一份名单——新增待确认键漏挂任
// 何一处都会让行绕过节流每 tick 重读大登记（复审 P2 的漂移源）。
func isWorktreePendingReason(reason string) bool {
	switch reason {
	case worktreeReasonRegistryIncomplete, worktreeReasonRemoveUncertain, worktreeReasonRemoveBudgetPending:
		return true
	}
	return false
}

// worktreeFinish 是单条运行回收终态出口的闭包形状，实现统一委托给
// finishWorktreeCleanup（ReviewService 的方法）——终态写行、释放计数、刷新
// 旧原因键与发新键聚合只有一个写法。
type worktreeFinish func(state model.ReviewWorktreeState, reason, errMsg string, notify bool)

// finishWorktreeCleanup 回收终态的唯一写入口（INT-170）：写终态行（removed
// 清账，其余落原因与错误）、释放进程内重试计数与退避排程、从问题态转移时
// 刷新旧原因键的聚合通知（受影响清单收缩）、按需发布新原因键。
// cleanupRunWorktree 的 finish 闭包、recheckBranchPending 的终态与
// archiveRunThread 的超限终态都走这里——手写 Updates+release+publish 的分叉
// 会把「刷新旧原因键」漏掉，同一裂缝以后还会再漏。
func (s *ReviewService) finishWorktreeCleanup(run *model.ReviewRun, state model.ReviewWorktreeState, reason, errMsg string, notify bool, now time.Time) {
	s.finishWorktreeCleanupExtra(run, state, reason, errMsg, notify, now, nil)
}

// finishWorktreeCleanupExtra 与 finishWorktreeCleanup 相同，额外把 extra 列
// 并入同一笔 Updates——archived_at 这类前置标记必须与终态原子落账，分开
// 写会留下半截状态（codex 复审）。
func (s *ReviewService) finishWorktreeCleanupExtra(run *model.ReviewRun, state model.ReviewWorktreeState, reason, errMsg string, notify bool, now time.Time, extra map[string]any) {
	updates := map[string]any{"worktree_state": state, "updated_at": now}
	for k, v := range extra {
		updates[k] = v
	}
	if state == model.ReviewWorktreeRemoved {
		// 恢复即清账：原因与错误不残留，聚合查询也不会再算上这行。
		updates["worktree_error"] = ""
		updates["worktree_reason"] = ""
	} else {
		updates["worktree_error"] = errMsg
		updates["worktree_reason"] = reason
	}
	if res := s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).Updates(updates); res.Error != nil {
		// 终态没写进去就中断：计数/退避不释放、内存态不改、聚合不发——
		// 下轮按库内旧态重走，通知不与库态背离（复审 Note）。
		s.logger.Warn("回收终态写库失败，下轮重试",
			zap.String("run", run.ID), zap.Error(res.Error))
		return
	}
	s.releaseWorktreeTries(run.ID)
	prior := run.WorktreeReason
	if state == model.ReviewWorktreeRemoved {
		run.WorktreeReason, run.WorktreeError = "", ""
	} else {
		run.WorktreeReason, run.WorktreeError = reason, errMsg
	}
	run.WorktreeState = state
	// 从问题态转移（原因变化或清空）时刷新旧原因键的聚合通知，受影响清单
	// 收缩；进入新的问题态再发新键。
	if prior != "" && prior != reason {
		s.publishCleanupAggregate(run, prior, now)
	}
	if notify {
		s.publishCleanupAggregate(run, reason, now)
	}
}

// cleanupRunWorktree 单条运行的工作区回收（INT-170 两步模型）：终态收尾 +
// 释放进程内状态。路径与分支真值来自线程投影（「空才补」回写行内，线程
// 之后 404 也不丢真值）；删除前经 verifyWorktreeOwnership 核验归属；分支步
// 只查登记 ref 名全集。任何失败都先重试/退避/待确认，到限才记 failed +
// 聚合通知；绝不删未经证明的路径。
func (s *ReviewService) cleanupRunWorktree(run *model.ReviewRun, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// branch_pending 行的复查：只查登记不读投影（分支被代理或人工删掉后
	// ref 整条消失，登记全集即真值），到退避点才查。
	if run.WorktreeState == model.ReviewWorktreeBranchPending {
		s.recheckBranchPending(ctx, run, now)
		return
	}
	// 持续待确认的行（登记未读完 / 删除结果待确认）按退避节流：未到点整个
	// 跳过——登记读取是大头，且这些行的远端真值在退避窗口内不会变，每 10s
	// 重读一遍大登记既浪费又给上游添堵。
	if isWorktreePendingReason(run.WorktreeReason) && !s.worktreeBackoffDue(run.ID, run.WorktreeReason, now) {
		return
	}

	finish := worktreeFinish(func(state model.ReviewWorktreeState, reason, errMsg string, notify bool) {
		s.finishWorktreeCleanup(run, state, reason, errMsg, notify, now)
	})

	// 从未发起派发（queued 就被取消）→ 没有线程与工作区可清。
	if run.DispatchStartedAt == nil {
		finish(model.ReviewWorktreeRemoved, "", "", false)
		return
	}
	// 确定没有建成线程的（dispatch_failed 请求帧未发出、pr_closed 建线程前
	// 就被拦下）：线程与工作区都未建成，无可停、无可归、无可删。
	if reviewRunNeverLaunched(run) {
		finish(model.ReviewWorktreeRemoved, "", "", false)
		return
	}
	// 归档前置：线程可能已建成的运行要等 archiveRuns 阶段归档完成；归档最终
	// 失败已由该阶段把本行置 failed（reason=archive_failed），不会走到这里。
	if run.ArchivedAt == nil {
		return
	}

	// 线程投影是工作区归属的唯一真值；读不到真值（T3 建 worktree 滞后于线程
	// 创建、上游暂时性失败）都按有限重试处理。
	threadWT, err := s.t3.ThreadWorktree(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			// 线程 404 不立即采信：可能来自 T3 重启/代理抖动，同样走有限重试，
			// 连续到上限才认定线程确已不存在。行内已有快照（此前回写的真值）
			// 就继续走登记核验——线程没了工作区可能还在；行内没有路径则磁盘
			// 工作区归 T3 生命周期管，removed（INT-170 前的旧语义）。
			if tries := s.bumpWorktreeTries(run.ID, worktreeTryNotFound); tries < reviewWorktreeMaxTries {
				return
			}
			if run.WorktreePath != "" {
				s.cleanupWithRegistry(ctx, run, run.WorktreePath, run.WorktreeBranch, true, now, finish)
				return
			}
			// 行内无路径但有分支快照（投影曾给出 branch 而 path 为空——root
			// 工作区线程或路径登记迟到）：分支可能仍存活，与活投影的同款场
			// 景一样走登记判定，不能直接收 removed 静默残留（复审 P2）。
			if run.WorktreeBranch != "" {
				s.cleanupWithRegistry(ctx, run, "", run.WorktreeBranch, true, now, finish)
				return
			}
			finish(model.ReviewWorktreeRemoved, "", "", false)
			return
		}
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryThread); tries < reviewWorktreeMaxTries {
			return
		}
		finish(model.ReviewWorktreeFailed, worktreeReasonThreadRead, "读取线程工作区失败: "+err.Error(), true)
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
	// 分支名「空才补」回写（与 worktree_path 同款）：分支步与线程 404 后的
	// 快照核验都依赖它。worktree_branch 是新增可空列，升级前已有的行是 NULL
	// ——等值条件匹配不到空串会静默写不进，随后误进 branch_pending、下轮
	// HasRef("")=false 又误收 removed，条件必须带 IS NULL。写失败（或条件未
	// 命中）与 path 同口径直接返回等下轮：不持久化就不进入依赖这份快照的
	// 删除与状态。
	if run.WorktreeBranch == "" && branch != "" {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND (worktree_branch = '' OR worktree_branch IS NULL)", run.ID).
			Updates(map[string]any{"worktree_branch": branch, "updated_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			s.logger.Warn("回写运行分支名失败，等下轮重试",
				zap.String("run", run.ID), zap.Error(res.Error))
			return
		}
		run.WorktreeBranch = branch
	}
	if path == "" {
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryEmptyPath); tries < reviewWorktreeMaxTries {
			return
		}
		// 线程确实没有 worktree（如视觉式 root 工作区）：分支也空 → 没有任何
		// 可清对象；分支非空（行内快照兜底）→ 跳过工作树步直接判分支。
		if branch == "" {
			branch = run.WorktreeBranch
		}
		// 投影仍无路径但行内有快照（登记迟到回收写回 / 旧行）：以行快照进完整
		// 回收链——登记会重新核验路径在册与否。用空路径跳步会让工作树步被跳
		// 过、分支仍在只回 branch_pending、下轮再转 none，永久打摆且工作树
		// 永不删（codex 复审）。
		if run.WorktreePath != "" {
			s.cleanupWithRegistry(ctx, run, run.WorktreePath, branch, false, now, finish)
			return
		}
		if branch == "" {
			finish(model.ReviewWorktreeRemoved, "", "", false)
			return
		}
		s.cleanupWithRegistry(ctx, run, "", branch, false, now, finish)
		return
	}
	// 行内旧快照与线程真值冲突：可能是行被并发改写或线程被复用，不删。
	if run.WorktreePath != "" && run.WorktreePath != path {
		finish(model.ReviewWorktreeFailed, worktreeReasonOwnershipMismatch, "运行记录的工作区与线程记录不一致，未执行删除", true)
		return
	}
	// 分支快照只在两边都非空且不等时判死（INT-170）：投影 branch 可空，重读
	// 丢字段拿到的空串不能推翻行内真值（verifyWorktreeOwnership 自己就容忍
	// branch 空）——投影为空时回落行内快照继续流程。路径字段的严格度不动：
	// 路径是删除对象本身，空与冲突都必须当真。
	if branch == "" {
		branch = run.WorktreeBranch
	} else if run.WorktreeBranch != "" && run.WorktreeBranch != branch {
		finish(model.ReviewWorktreeFailed, worktreeReasonOwnershipMismatch, "运行记录的分支与线程记录不一致，未执行删除", true)
		return
	}
	// 把线程真值写回运行行（只在仍为空时补，不覆盖其他写入；worktree_path
	// 同样受旧库 NULL 影响，条件带 IS NULL）。写失败就等下个 tick 重试：行里
	// 仍是空路径时核验的行快照必然不符，会把一次瞬时 DB 失败误报成「归属核验
	// 未通过」。
	if run.WorktreePath == "" {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND (worktree_path = '' OR worktree_path IS NULL)", run.ID).
			Updates(map[string]any{"worktree_path": path, "updated_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			// 与分支写回同口径（复审 Note）：条件未命中=并发写入先行，不能
			// 当写成功继续——行内内存态会与库背离。
			s.logger.Warn("回写运行工作区路径失败，等下轮重试",
				zap.String("run", run.ID), zap.Error(res.Error))
			return
		}
		run.WorktreePath = path
	}
	s.cleanupWithRegistry(ctx, run, path, branch, false, now, finish)
}

// cleanupWithRegistry 工作区回收的主体：读登记 → 工作树步（归属核验 + 删除）
// → 分支步（ref 名全集核验）。path 为空表示线程确认没有工作树（或线程 404
// 且行内无路径快照），跳过工作树步只判分支。snapshotOnly 表示 path/branch
// 来自行内快照而非活着的线程投影（线程 404）——此时分支名缺失无法证明，
// 与「投影明说没分支」要分开处理。
func (s *ReviewService) cleanupWithRegistry(ctx context.Context, run *model.ReviewRun, path, branch string, snapshotOnly bool, now time.Time, finish worktreeFinish) {
	// 核验与删除用同一个 cwd：launch 时快照的 workspace_root 优先——绑定之后
	// 被删/改时，当前绑定不能代表当时的工作区根目录。
	cwd := s.worktreeCwdFor(run)
	if cwd == "" {
		finish(model.ReviewWorktreeFailed, worktreeReasonEnvBinding, "环境绑定缺失，无法定位工作区", true)
		return
	}
	reg, err := s.t3.ListRegistry(ctx, run.UserID, run.EnvironmentID, cwd)
	if err != nil {
		s.handleRegistryReadError(run, err, now, finish)
		return
	}
	// 登记读成功：待确认态（registry_incomplete / remove_uncertain）解除——
	// 清行内原因与计数并刷新旧原因键的聚合通知（受影响清单收缩）。登记错误
	// 计数同步清零：重试宽限按「连续失败」计，中途成功必须复位，否则常驻
	// pending 行会被跨天瞬时抖动累计成永久 failed。
	s.resetWorktreeTries(run.ID, worktreeTryRegistry)
	s.recoverFromPending(run, now)

	if path != "" && !s.worktreeStep(ctx, run, path, branch, cwd, reg, now, finish) {
		return
	}
	// 分支步：行内没有分支名 → 收 removed；分支仍在登记 ref 全集 →
	// branch_pending（服务端删不了，等代理收尾或人工）；不在 → removed。
	ambiguousRefs := false
	branchInferred := false
	if branch == "" && path != "" {
		// 分支名缺失但登记把该路径绑定到具体分支（升级前 worktree_branch 为
		// NULL 的旧行、线程 404 后只剩行快照）：借登记恢复——不恢复就收
		// removed，仍存活的分支从此无人追踪（codex 复审）。同路径登记多个
		// ref 名视为歧义，不采用。
		if idx := slices.IndexFunc(reg.Worktrees, func(w T3Worktree) bool { return w.Path == path }); idx >= 0 {
			if len(reg.Worktrees[idx].RefNames) == 1 {
				candidate := reg.Worktrees[idx].RefNames[0]
				// 最低佐证（复审 Note）：唯一 ref 若同时绑在别的 worktree
				// 路径，说明该分支被别处 checkout、并非本运行已删工作树的
				// 专属分支——按歧义处理，不盲采信去跟踪无关分支。
				if slices.ContainsFunc(reg.Worktrees, func(w T3Worktree) bool {
					return w.Path != path && slices.Contains(w.RefNames, candidate)
				}) {
					ambiguousRefs = true
				} else {
					branch, branchInferred = candidate, true
				}
			} else if len(reg.Worktrees[idx].RefNames) > 1 {
				ambiguousRefs = true
			}
		}
	}
	if branch == "" {
		if path != "" && (snapshotOnly || ambiguousRefs) {
			// 无法证明「没有专属分支残留」的两类场景都记 failed 交人工，不收
			// removed 撒谎（codex/bot 复审）：a) 只剩行快照（线程 404、升级
			// 前 NULL 行），工作树先一步消失，分支名缺失且登记无法恢复；
			// b) 登记把该路径绑定到多个 ref 名，无法唯一映射本运行专属分支。
			// 活着的投影明说 branch="" 且登记路径无绑定 ref 才是权威「没分
			// 支」，不在这列。
			finish(model.ReviewWorktreeFailed, worktreeReasonBranchUnknown,
				"分支名缺失且登记无法唯一映射本运行专属分支，无法确认是否已清理，请按工作区路径人工核查", true)
			return
		}
		finish(model.ReviewWorktreeRemoved, "", "", false)
		return
	}
	if reg.HasRef(branch) {
		// 进入 branch_pending 前断言分支快照已在行内持久化（本来非空或刚写
		// 成功）：复查只认行内的 worktree_branch，空快照进 pending 后下轮
		// HasRef("")=false 会误收 removed。登记刚恢复的名字同样走这条「空才
		// 补」写回，写失败就等下轮，不持久化不进 pending。
		if run.WorktreeBranch == "" {
			res := s.db.Model(&model.ReviewRun{}).
				Where("id = ? AND (worktree_branch = '' OR worktree_branch IS NULL)", run.ID).
				Updates(map[string]any{"worktree_branch": branch, "updated_at": now})
			if res.Error != nil || res.RowsAffected == 0 {
				s.logger.Warn("回写登记恢复的分支名失败，等下轮重试",
					zap.String("run", run.ID), zap.Error(res.Error))
				return
			}
			run.WorktreeBranch = branch
		}
		msg := "工作树已删除，本轮专属分支 " + branch + " 仍存在；当前远程 T3 无删除分支接口，待审查代理收尾或人工清理"
		if branchInferred {
			msg = "工作树已删除，登记推断的本轮专属分支 " + branch + "（名由登记恢复，未经线程确认）仍存在；当前远程 T3 无删除分支接口，待审查代理收尾或人工清理"
		}
		finish(model.ReviewWorktreeBranchPending, worktreeReasonBranchPending, msg, true)
		return
	}
	finish(model.ReviewWorktreeRemoved, "", "", false)
}

// handleRegistryReadError 登记读取失败的分流：预算耗尽（含 ctx 取消）是
// 「待确认」不是失败——保持 none 挂退避，连续到上限起发聚合通知；上游明确
// 报错走有限重试，超限 failed + 聚合通知（现有行为口径）。
func (s *ReviewService) handleRegistryReadError(run *model.ReviewRun, err error, now time.Time, finish worktreeFinish) {
	if errors.Is(err, errRegistryIncomplete) {
		if tries := s.markBudgetPending(run, worktreeReasonRegistryIncomplete, "登记读取未在预算内完成，待下轮确认重试", now); tries >= reviewBudgetPendingMaxTries {
			finish(model.ReviewWorktreeFailed, worktreeReasonRegistryError,
				"登记读取长时间未在预算内完成，按失败处理", true)
		}
		return
	}
	if tries := s.bumpWorktreeTries(run.ID, worktreeTryRegistry); tries < reviewWorktreeMaxTries {
		return
	}
	finish(model.ReviewWorktreeFailed, worktreeReasonRegistryError, "读取项目工作区登记失败: "+err.Error(), true)
}

// markBudgetPending 把「预算在行内耗尽」的暂态落行：保持 none 挂退避，连
// 续到上限起发聚合通知（进入持续待确认态）。登记读取与删除前置（env/dial
// 在调用方预算内到点）共用——同一根因同一口径（复审 Note）。返回连续失败
// 计数，供调用方按 reviewBudgetPendingMaxTries 收敛 failed（待确认不是无
// 限期状态）。
func (s *ReviewService) markBudgetPending(run *model.ReviewRun, reason, errMsg string, now time.Time) int {
	// 两类预算耗尽分键计数（复审 P2）：登记侧由登记读成功复位，删除侧只有
	// RemoveWorktree 的后续结果能消解——共键会让删除侧被登记读清零。
	tryKey := worktreeTryRegIncomplete
	if reason == worktreeReasonRemoveBudgetPending {
		tryKey = worktreeTryRemoveBudget
	}
	tries := s.bumpWorktreeTries(run.ID, tryKey)
	s.scheduleWorktreeBackoff(run.ID, reason, now)
	if !s.writePendingReason(run, reason, errMsg, now) {
		return tries
	}
	if tries >= reviewWorktreeMaxTries {
		// 进入持续待确认态：按（环境, 仓库, 原因）聚合通知（同键重复
		// 发布由 dedup+指纹收敛，正文随清单自动更新）。
		s.publishCleanupAggregate(run, reason, now)
	}
	return tries
}

// recoverFromPending 登记读成功后清除行内的待确认原因（INT-170）：计数按
// 「连续」语义清零（registry_incomplete 的重试宽限重新起算）。登记可读即
// 该原因确实解除，重发一次聚合通知刷新受影响清单——两条待确认原因都发：
// reason 在这里已清掉，不补发旧键，它的受影响清单会永远滞留本运行（发布面
// 已处理「键不存在且清单为空→跳过」，不为从未发布的键造恢复通知）。
// remove_uncertain 的计数不清——它的上限跨越多个「登记成功」的 tick，清了
// 就永远到不了终态。
func (s *ReviewService) recoverFromPending(run *model.ReviewRun, now time.Time) {
	if run.WorktreeReason == "" || run.WorktreeReason == worktreeReasonRemoveBudgetPending {
		// remove_budget_pending 不由登记读消解（复审 P2）：登记读成功不代表
		// 删除预算恢复，它的清算点是本 tick RemoveWorktree 的重试结果。这里
		// 清了会让原因每 tick 抖动一次落库+空发聚合，还会连带复位计数。
		return
	}
	prior := run.WorktreeReason
	if err := s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"worktree_reason": "", "worktree_error": "", "updated_at": now}).Error; err != nil {
		// 写失败直接中断、不动内存态（复审 P2）：内存先于库态清空会让
		// finish 读不到 prior、跳过旧键聚合收缩，旧键清单永久滞留本运行。
		s.logger.Warn("清除待确认原因写库失败", zap.String("run", run.ID), zap.Error(err))
		return
	}
	run.WorktreeReason = ""
	s.resetWorktreeTries(run.ID, worktreeTryRegIncomplete)
	// 退避排程按 prior reason 一并清（INT-170）：恢复后再入同一待确认态时从
	// 起步间隔重新爬梯，不接着半截阶梯跳档。
	s.clearWorktreeBackoff(run.ID, prior)
	// 旧原因键的聚合通知重发收缩清单（INT-170 复审修订：remove_uncertain 同
	// 样需要——reason 在本函数已清掉，finish 读不到 prior，这里不重发该键
	// 的受影响清单会永远滞留本运行）。发布面已处理「键不存在且清单为空
	// →跳过」，不会为从未发布的键造恢复通知。
	if prior != "" {
		s.publishCleanupAggregate(run, prior, now)
	}
}

// writePendingReason 把「保持 none 的待确认/暂态原因」落行：状态不变，只更新
// 原因与错误，供聚合通知查询受影响清单。行内已有不同的待确认原因时按旧键
// 重发一次聚合——不补发，旧键的受影响清单会永久滞留本运行（复审 Note）。
// 返回写库是否成功：失败时不动内存态、不重发旧键（复审 P2——内存先于库态
// 改写会让 finish 读不到 prior，旧键清单永久滞留）。
func (s *ReviewService) writePendingReason(run *model.ReviewRun, reason, errMsg string, now time.Time) bool {
	prior := run.WorktreeReason
	if err := s.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{
			"worktree_state":  model.ReviewWorktreeNone,
			"worktree_reason": reason, "worktree_error": errMsg, "updated_at": now,
		}).Error; err != nil {
		s.logger.Warn("待确认原因写库失败", zap.String("run", run.ID), zap.Error(err))
		return false
	}
	run.WorktreeReason, run.WorktreeError = reason, errMsg
	if prior != "" && prior != reason {
		s.publishCleanupAggregate(run, prior, now)
	}
	return true
}

// worktreeStep 工作树步：登记命中才核验归属并删除。返回 false 表示本 tick
// 在步内收尾（failed 或挂起待确认），调用方直接返回；true 表示工作树步完成
// （已删、本就不在或待确认但有界重试尚未到限），可以进入分支步。
func (s *ReviewService) worktreeStep(ctx context.Context, run *model.ReviewRun, path, branch, cwd string, reg *T3Registry, now time.Time, finish worktreeFinish) bool {
	if !reg.ContainsWorktree(path) {
		// 不在登记中（代理收尾已删、或从未建成/被用户删掉）：工作树步完成，
		// 不细究是哪种——登记无即无可删。
		return true
	}
	verdict, reason, err := s.verifyWorktreeOwnership(run, path, branch, reg)
	if err != nil {
		// 暂时读不到（运行行/认领统计）：有限重试，超限记失败。与登记读取
		// 分键（worktreeTryOwnership）——登记读成功每 tick 复位 registry 键，
		// 共用会让核验错误的宽限永远刷回 1、持续失败永远到不了上限。
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryOwnership); tries < reviewWorktreeMaxTries {
			return false
		}
		finish(model.ReviewWorktreeFailed, worktreeReasonOwnershipError, err.Error(), true)
		return false
	}
	// 归属核验读成功复位其连续失败计数。
	s.resetWorktreeTries(run.ID, worktreeTryOwnership)
	switch verdict {
	case worktreeGone:
		return true
	case worktreeMismatch:
		finish(model.ReviewWorktreeFailed, worktreeReasonOwnershipMismatch, reason, true)
		return false
	}
	if err := s.t3.RemoveWorktree(ctx, run.UserID, run.EnvironmentID, cwd, path, true); err == nil {
		return true
	} else if errors.Is(err, errRegistryIncomplete) {
		// 删除前置（env/dial）在调用方预算内到点：与登记读取同口径「待确认」
		// 挂退避——上游慢但活着不是「未送达」，不占三次重试就落 failed（复审
		// Note）。连续累计超限同样收敛 failed，待确认不是无限期状态。原因
		// 用 remove_budget_pending 与登记读取分键（复审 P2），聚合通知才不
		// 会把删除侧根因错标成「登记读取不完整」。
		if tries := s.markBudgetPending(run, worktreeReasonRemoveBudgetPending, "执行删除的时间预算在行内耗尽，待下轮重试", now); tries >= reviewBudgetPendingMaxTries {
			finish(model.ReviewWorktreeFailed, worktreeReasonWorktreeRemove,
				"执行删除的时间预算长时间耗尽，按失败处理", true)
		}
		return false
	} else if errors.Is(err, errT3RemoveUncertain) {
		// 结果不可见（读超时/连接中断）：先重读一次登记复查远端实际状态——
		// 已不在即完成（远端执行完了才报错）；仍在或复查不到就保持 none 挂
		// 退避，计数到上限才 failed。每次重发删除前都会重过归属核验（下个
		// tick 从头走）。
		if s.confirmWorktreeGoneAfterRemove(ctx, run, cwd, path) {
			return true
		}
		tries := s.bumpWorktreeTries(run.ID, worktreeTryRemoveUncertain)
		s.scheduleWorktreeBackoff(run.ID, worktreeReasonRemoveUncertain, now)
		s.writePendingReason(run, worktreeReasonRemoveUncertain, "删除工作区结果待确认（超时或连接中断），下轮复查远端状态", now)
		if tries >= reviewWorktreeMaxTries {
			finish(model.ReviewWorktreeFailed, worktreeReasonRemoveUncertain,
				"删除工作区结果多次不可见，停止重试；远端状态请人工确认", true)
		}
		return false
	} else if errors.Is(err, errT3RemoveNotAttempted) {
		// 请求未送达（环境/凭证/拨号失败，INT-170）：远端必然没执行——无需
		// 复查登记（复查也读不出「没发生」的删除），按有限重试处理，到限才
		// failed + 聚合。reason 仍用 worktree_remove（对外不新增值），文案写
		// 明是未送达而非被远端拒绝。
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryRemoveAttempt); tries < reviewWorktreeMaxTries {
			return false
		}
		finish(model.ReviewWorktreeFailed, worktreeReasonWorktreeRemove,
			"删除工作区请求未能送达远端（环境或连接不可用），停止重试", true)
		return false
	} else {
		// 确定失败：也先重读一次登记——路径已不在说明远端其实删掉了（报错
		// 与执行竞态）；仍在才记失败。
		if s.confirmWorktreeGoneAfterRemove(ctx, run, cwd, path) {
			return true
		}
		finish(model.ReviewWorktreeFailed, worktreeReasonWorktreeRemove, "删除工作区失败: "+err.Error(), true)
		return false
	}
}

// confirmWorktreeGoneAfterRemove 删除报错后的复查：重读一次登记，路径已不在
// 返回 true（目标态已达成）。复查本身失败按「仍在」处理——不能把读不到登记
// 当成删除成功。
func (s *ReviewService) confirmWorktreeGoneAfterRemove(ctx context.Context, run *model.ReviewRun, cwd, path string) bool {
	if ctx.Err() != nil {
		// 行预算已被刚失败的删除耗尽：这次复查注定翻不完页返回
		// incomplete，查了也形同虚设——直接按「仍在」等下个退避周期（复审
		// Note）。
		return false
	}
	reg, err := s.t3.ListRegistry(ctx, run.UserID, run.EnvironmentID, cwd)
	if err != nil || reg.ContainsWorktree(path) {
		return false
	}
	return true
}

// recheckBranchPending branch_pending 行的复查（INT-170）：到退避点只查登记
// （不读投影）——分支被代理收尾或人工删掉后 ref 整条消失，ref 名全集即真值。
// 消失 → removed 并刷新该原因键的聚合通知（受影响清单收缩）；仍在 → 保持并
// 续排退避。
func (s *ReviewService) recheckBranchPending(ctx context.Context, run *model.ReviewRun, now time.Time) {
	if !s.worktreeBackoffDue(run.ID, worktreeReasonBranchPending, now) {
		return
	}
	cwd := s.worktreeCwdFor(run)
	if cwd == "" {
		// 绑定缺失证明不了分支已删：保持 branch_pending 续排退避——分支仍在
		// 的事实没变，改判 failed 反而丢掉「分支待清理」的语义。
		s.logger.Warn("branch_pending 复查缺少工作区根目录，保持待复查",
			zap.String("run", run.ID), zap.String("branch", run.WorktreeBranch))
		s.scheduleWorktreeBackoff(run.ID, worktreeReasonBranchPending, now)
		return
	}
	reg, err := s.t3.ListRegistry(ctx, run.UserID, run.EnvironmentID, cwd)
	if err != nil {
		if errors.Is(err, errRegistryIncomplete) {
			// 预算耗尽与 none 态同口径收敛（复审 P2）：复用同一计数键与上限，
			// 超限按长期读不完整记 failed——待确认不是无限期状态，branch_
			// pending 行也不能例外地永久重读大登记。
			if tries := s.bumpWorktreeTries(run.ID, worktreeTryRegIncomplete); tries < reviewBudgetPendingMaxTries {
				s.scheduleWorktreeBackoff(run.ID, worktreeReasonBranchPending, now)
				return
			}
			s.finishWorktreeCleanup(run, model.ReviewWorktreeFailed, worktreeReasonRegistryError,
				"复查分支登记长时间未在预算内完成，按失败处理", true, now)
			return
		}
		// 上游明确报错：有限重试，超限降级 failed（复查面坏掉，分支残留的
		// 语义已经在进入 branch_pending 时通知过）。终态统一走
		// finishWorktreeCleanup——branch_pending 旧键随转移自动刷新，避免
		// 手写 Updates 漏掉旧键的清单收缩。
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryRegistry); tries < reviewWorktreeMaxTries {
			s.scheduleWorktreeBackoff(run.ID, worktreeReasonBranchPending, now)
			return
		}
		s.finishWorktreeCleanup(run, model.ReviewWorktreeFailed, worktreeReasonRegistryError,
			"复查分支登记失败: "+err.Error(), true, now)
		return
	}
	// 登记读成功复位连续失败计数：branch_pending 行生命周期无上限，瞬时抖动
	// 不reset会跨天累计成永久 failed。
	s.resetWorktreeTries(run.ID, worktreeTryRegistry)
	s.resetWorktreeTries(run.ID, worktreeTryRegIncomplete)
	// T3 建 worktree 可能滞后于首次回收（首轮按 path 空直接判分支进 pending）：
	// 登记出现绑定本分支的 worktree 项时回到完整回收链路处理——行内无路径先按
	// 「空才补」写回（写失败保持待复查），随后转 none 让下个 tick 走归属核验+
	// 删除+分支步（核验与删除仍同源，不在这里直接删）。行内已有快照路径时：
	// 同一路径说明回收链曾走到过这步（或路径被删后重现），转 none 由完整核验
	// 决定；全部绑定都在异路径则归属不明，记 failed 交人工。
	// 同一分支可能被绑到多个 worktree：先收集全部绑定再定夺——行内已有快
	// 照路径时优先采信同路径项恢复，只有全部绑定都在异路径才算归属不明
	// （复审 Note：首个命中即定夺会把「同路径+异路径并存」的合法情形按切
	// 片顺序错判成永久 failed）。
	var bound []T3Worktree
	for _, w := range reg.Worktrees {
		if slices.Contains(w.RefNames, run.WorktreeBranch) {
			bound = append(bound, w)
		}
	}
	if len(bound) > 0 {
		match := -1
		if run.WorktreePath == "" {
			// 行内无路径快照时只有唯一绑定才可采信：同分支可被绑到多个不
			// 同路径的 worktree（同分支上另建、旧登记残留），bound[0] 按路
			// 径排序不代表是本运行的——盲信会把别人的工作树写回并删掉
			// （复审 P2）。多绑定无快照 = 归属不明，交人工。
			if len(bound) == 1 {
				match = 0
			}
		} else if i := slices.IndexFunc(bound, func(w T3Worktree) bool { return w.Path == run.WorktreePath }); i >= 0 {
			match = i
		}
		if match < 0 {
			// 归属不明：不删（歧义资源），也不无限续排（无信号的永久滞
			// 留，复审 P2），记 failed 交人工。两种歧义文案分开——行内有快
			// 照路径是「与记录冲突」，无快照多绑定是「无法指认」。
			msg := "本轮分支 " + run.WorktreeBranch + " 被绑定到工作区 " + bound[0].Path + "，与记录不一致，未执行删除"
			if run.WorktreePath == "" {
				paths := make([]string, 0, len(bound))
				for _, w := range bound {
					paths = append(paths, w.Path)
				}
				msg = "本轮分支 " + run.WorktreeBranch + " 被绑定到多个工作区（" + strings.Join(paths, ", ") + "），行内无路径快照无法指认，未执行删除"
			}
			s.finishWorktreeCleanup(run, model.ReviewWorktreeFailed, worktreeReasonOwnershipMismatch, msg, true, now)
			return
		}
		if run.WorktreePath == "" {
			res := s.db.Model(&model.ReviewRun{}).
				Where("id = ? AND (worktree_path = '' OR worktree_path IS NULL)", run.ID).
				Updates(map[string]any{"worktree_path": bound[match].Path, "updated_at": now})
			if res.Error != nil || res.RowsAffected == 0 {
				s.logger.Warn("补登记迟到的回收工作区路径失败，保持待复查",
					zap.String("run", run.ID), zap.Error(res.Error))
				s.scheduleWorktreeBackoff(run.ID, worktreeReasonBranchPending, now)
				return
			}
			run.WorktreePath = bound[match].Path
		}
		// 转 none 会释放全部重试计数——branch-pending 的「确认存活」累计要
		// 保留（复审 Note）：T3 侧对同一分支反复出现/消失 worktree 登记
		// 时，24 次上限被反复重置会把收敛时长无限拉长。
		s.worktreeMu.Lock()
		bpTries := s.worktreeTries[run.ID+"|"+worktreeTryBranchPending]
		s.worktreeMu.Unlock()
		s.finishWorktreeCleanup(run, model.ReviewWorktreeNone, "", "", false, now)
		if bpTries > 0 {
			s.worktreeMu.Lock()
			s.worktreeTries[run.ID+"|"+worktreeTryBranchPending] = bpTries
			s.worktreeMu.Unlock()
		}
		return
	}
	if reg.HasRef(run.WorktreeBranch) {
		// 分支仍存活：续排等代理收尾或人工，但确认存活有上限——封顶 1h 退避
		// 下 reviewBranchPendingMaxTries 次（≈1 天）仍未消失说明不会再有人
		// 清，收敛 failed 交人工而非永久滞留（复审 Note）。
		if tries := s.bumpWorktreeTries(run.ID, worktreeTryBranchPending); tries < reviewBranchPendingMaxTries {
			s.scheduleWorktreeBackoff(run.ID, worktreeReasonBranchPending, now)
			return
		}
		s.finishWorktreeCleanup(run, model.ReviewWorktreeFailed, worktreeReasonBranchPending,
			"本轮专属分支 "+run.WorktreeBranch+" 超过复查上限仍存活；当前远程 T3 无删除分支接口，请人工清理", true, now)
		return
	}
	// 分支消失：removed 并刷新 branch_pending 键的聚合通知（受影响清单收缩）
	// ——由 finishWorktreeCleanup 按 prior reason 统一处理。
	s.finishWorktreeCleanup(run, model.ReviewWorktreeRemoved, "", "", false, now)
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
// 运行行快照（repo/pr/head/thread/path）→ 其他运行认领 → 登记（调用方刚读好
// 的同一份快照，INT-170 起不再内部二次读取——核验与删除的登记视图必须同源）
// → ref 名与线程分支一致。error 非空表示暂时读不到（可重试），verdict 无效；
// mismatch 时 reason 给出人读得懂的原因。
func (s *ReviewService) verifyWorktreeOwnership(run *model.ReviewRun, path, branch string, reg *T3Registry) (worktreeVerdict, string, error) {
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
	// 是历史终态、branch_pending 的工作树已删，路径都只是残留快照，把它们算
	// 认领者会让后续同路径的合法属主永久 failed。
	var other int64
	if err := s.db.Model(&model.ReviewRun{}).
		Where("worktree_path = ? AND id <> ? AND worktree_state = ?", path, run.ID, model.ReviewWorktreeNone).
		Count(&other).Error; err != nil {
		return 0, "", fmt.Errorf("统计工作区认领失败: %w", err)
	}
	if other > 0 {
		return worktreeMismatch, "工作区归属核验未通过，未执行删除", nil
	}
	// 登记核验：该 worktree 必须仍登记在案——防误删已被另作他用的同路径
	// 工作区（登记由调用方传入，与后续删除同一份视图）。
	idx := slices.IndexFunc(reg.Worktrees, func(w T3Worktree) bool { return w.Path == path })
	if idx < 0 {
		return worktreeGone, "", nil
	}
	// 分支核验：线程投影的 branch 可空（F4 前缀的误判已修），非空时才要求
	// 该路径登记的 ref 名集合包含它；为空时只凭路径命中即认定归属。
	if branch != "" && !slices.Contains(reg.Worktrees[idx].RefNames, branch) {
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

// worktreeBackoffEntry 一条退避排程：下次允许尝试时刻 + 已排次数（次数驱动
// 指数增长）。
type worktreeBackoffEntry struct {
	nextAt   time.Time
	attempts int
}

// 持续待确认问题（registry_incomplete / remove_uncertain 重试、branch_pending
// 复查）的退避节奏：起步 30s 指数增长。重试类封顶 5min——每 10s tick 重读大
// 登记既浪费又给上游添堵；branch_pending 是长期等外部动作（代理收尾或人工
// 删分支），分钟级的反复重查没有意义，封顶 1h。进程重启丢失退避表无所谓
// （下个 tick 即可重查）。
const (
	worktreeBackoffBase = 30 * time.Second
	worktreeBackoffCap  = 5 * time.Minute
	// worktreeBackoffCapBranchPending branch_pending 复查的退避封顶。
	worktreeBackoffCapBranchPending = time.Hour
)

// worktreeBackoffCapFor 按原因取退避封顶。
func worktreeBackoffCapFor(reason string) time.Duration {
	if reason == worktreeReasonBranchPending {
		return worktreeBackoffCapBranchPending
	}
	return worktreeBackoffCap
}

// worktreeBackoffDue 报告该键是否已到退避点（无排程视为到期）。
func (s *ReviewService) worktreeBackoffDue(runID, reason string, now time.Time) bool {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	e, ok := s.worktreeBackoff[runID+"|"+reason]
	return !ok || !now.Before(e.nextAt)
}

// scheduleWorktreeBackoff 排下一次允许尝试的时刻：30s 起指数增长，封顶按原因
// 取（branch_pending 1h，其余 5min）。爬梯到封顶即停住；attempts 继续累计
// 无妨——封顶之后每档都是同一个值。
func (s *ReviewService) scheduleWorktreeBackoff(runID, reason string, now time.Time) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	key := runID + "|" + reason
	e := s.worktreeBackoff[key]
	if e == nil {
		e = &worktreeBackoffEntry{}
		s.worktreeBackoff[key] = e
	}
	cap := worktreeBackoffCapFor(reason)
	delay := worktreeBackoffBase
	for i := 0; i < e.attempts && delay < cap; i++ {
		delay <<= 1
	}
	if delay > cap {
		delay = cap
	}
	e.attempts++
	e.nextAt = now.Add(delay)
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
// 只管计数表——计数键与退避键是两个名空间（计数用 try 常量、退避用 reason
// 常量），要清退避排程用 clearWorktreeBackoff。
func (s *ReviewService) resetWorktreeTries(runID, reason string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	delete(s.worktreeTries, runID+"|"+reason)
}

// clearWorktreeBackoff 清掉一个运行某个原因的退避排程：恢复后再入同一待确认
// 态时从起步间隔重新爬梯（INT-170），不接着半截阶梯跳档。
func (s *ReviewService) clearWorktreeBackoff(runID, reason string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	delete(s.worktreeBackoff, runID+"|"+reason)
}

// releaseWorktreeTries 释放一个运行的回收重试计数与退避排程：归档/回收走到
// 终态时调用，防泄漏。重试中的分支不得调用（否则下个 tick 重新计，上限形同
// 虚设）。
func (s *ReviewService) releaseWorktreeTries(runID string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	prefix := runID + "|"
	for key := range s.worktreeTries {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(s.worktreeTries, key)
		}
	}
	for key := range s.worktreeBackoff {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(s.worktreeBackoff, key)
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
// 核验与常规回收共用 verifyWorktreeOwnership，owned 才删除。分支没有删除
// RPC（INT-170）：删除后分支仍登记在案只记日志，不阻断项目删除。
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
	// 与常规回收同口径：线程真值写回运行行（仅空时补；worktree_path 同样受
	// 旧库 NULL 影响，条件带 IS NULL），核验的行快照才有据可比。
	if run.WorktreePath == "" {
		if err := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND (worktree_path = '' OR worktree_path IS NULL)", run.ID).
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
	reg, err := s.t3.ListRegistry(ctx, run.UserID, run.EnvironmentID, cwd)
	if err != nil {
		s.logger.Warn("删除项目前读取工作区登记失败，跳过回收", zap.String("run", run.ID), zap.Error(err))
		return
	}
	verdict, reason, err := s.verifyWorktreeOwnership(run, path, branch, reg)
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
			return
		}
		if branch != "" && reg.HasRef(branch) {
			s.logger.Info("删除项目后本轮分支仍登记在案（无删除分支 RPC，留给代理收尾或人工清理）",
				zap.String("run", run.ID), zap.String("branch", branch))
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

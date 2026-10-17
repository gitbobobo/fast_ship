package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 定时任务调度（INT-103）：单进程一个 10s ticker，与 AutomationScheduler 同一
// 纪律——数据库是事实来源，MaxOpenConns(1) 下事务内绝不发网络调用。每个
// tick 短事务内逐任务处理：到期 →（错过超宽限记 skipped | 建运行行）→ CAS
// 推进 next_run_at；随后事务外唤醒执行槽。重启不补跑：错过的计划时间按
// 「跳过并记录」处理，未完成的 schedule run 由启动恢复统一置 failed。

const (
	botScheduleTickInterval = 10 * time.Second
	// botScheduleMissGrace 是计划时间的错过宽限：超过即视为错过（须大于
	// tick 间隔，容忍调度抖动）。
	botScheduleMissGrace = 90 * time.Second
	// botScheduleDispatchBatch 单轮最多处理的到期任务数。
	botScheduleDispatchBatch = 50
	// botScheduleCallSettleWait 是等待本 run 异步调用结算的上限：单次异步调用
	// 自身的执行超时是 botAsyncExecTimeout，这里多给一段余量；超过就显式失败，
	// 不谎报成功。
	botScheduleCallSettleWait = botAsyncExecTimeout + 5*time.Minute
	// botScheduleCallSettlePoll 是等待结算的轮询间隔。
	botScheduleCallSettlePoll = 500 * time.Millisecond
)

// scheduleWaitOutcome 是等待本次运行异步调用结算的结果。
type scheduleWaitOutcome int

const (
	scheduleWaitSettled scheduleWaitOutcome = iota
	scheduleWaitStopped
	scheduleWaitTimeout
)

// waitScheduleCallsSettled 等到本次 schedule run 的异步写都落终态。
// 等待期间不发模型请求（否则模型会看不到新内容反复空跑），结算后循环重建
// 上下文，模型就能据实汇报调用的真实成败。
func (s *BotService) waitScheduleCallsSettled(ctx context.Context, run *model.BotRun) scheduleWaitOutcome {
	return s.waitScheduleCallsSettledUntil(ctx, run, time.Now().Add(botScheduleCallSettleWait))
}

// waitScheduleCallsSettledUntil 同 waitScheduleCallsSettled，deadline 可注入。
func (s *BotService) waitScheduleCallsSettledUntil(ctx context.Context, run *model.BotRun, deadline time.Time) scheduleWaitOutcome {
	for {
		unsettled, err := s.calls.CountUnsettledByRun(run.ID)
		if err != nil {
			// 查询失败不阻塞运行：宁可让模型看旧结果，也不把一次瞬时数据库
			// 抖动变成运行失败。
			s.logger.Warn("查询未结算调用失败", zap.String("run_id", run.ID), zap.Error(err))
			return scheduleWaitSettled
		}
		if unsettled == 0 {
			return scheduleWaitSettled
		}
		if s.safePoint(run).StopRequested {
			return scheduleWaitStopped
		}
		if time.Now().After(deadline) {
			return scheduleWaitTimeout
		}
		select {
		case <-ctx.Done():
			return scheduleWaitStopped
		case <-time.After(botScheduleCallSettlePoll):
		}
	}
}

// scheduleLoop 每 10s 扫描一次到期任务；启动后立即跑一轮（错过处理由
// next_run_at 与宽限决定，不依赖停机时长）。
func (s *BotService) scheduleLoop() {
	ticker := time.NewTicker(botScheduleTickInterval)
	defer ticker.Stop()
	s.scheduleTick(time.Now().UTC())
	for range ticker.C {
		s.scheduleTick(time.Now().UTC())
	}
}

func (s *BotService) scheduleTick(now time.Time) {
	due, err := s.schedTasks.ListDue(now, botScheduleDispatchBatch)
	if err != nil {
		s.logger.Warn("扫描到期定时任务失败", zap.Error(err))
		return
	}
	for i := range due {
		s.dispatchDueScheduleTask(&due[i], now)
	}
}

// dispatchDueScheduleTask 处理一个到期任务：短事务内核对 enabled + 旧
// next_run_at 未变，写 skipped/running 运行行并在同一事务内 CAS 推进
// next_run_at。重复触发由 UNIQUE(bot_id, trigger_key) 挡下（0 行推进 = 被并发
// 抢走，直接跳过）。
func (s *BotService) dispatchDueScheduleTask(task *model.BotScheduleTask, now time.Time) {
	planned := *task.NextRunAt
	wakeRunID := ""
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var current model.BotScheduleTask
		if err := tx.Where("id = ? AND enabled = ?", task.ID, true).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 已被停用/删除，跳过
			}
			return err
		}
		if current.NextRunAt == nil || !current.NextRunAt.Equal(planned) {
			return nil // next_run_at 已被并发推进或重排，跳过
		}

		// 派发时冻结有效配置：同事务读一次机器人记下当时的 model/base_url，
		// wakeScheduleRun 之后（载入执行槽才 Get 机器人）即使配置被编辑，
		// 这次运行仍按快照执行——与主线 ensureRun 的 botSnapshotOf 同语义。
		// 只做一次主键查询，不发网络调用。
		var botRow model.Bot
		if err := tx.Where("id = ?", current.BotID).First(&botRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 机器人已删除（任务会级联删除），跳过
			}
			return err
		}
		snap := botSnapshotOf(&botRow)
		if current.Model != "" {
			snap.Model = current.Model
		}

		run := &model.BotRun{
			ID: uuid.NewString(), BotID: current.BotID,
			TriggerSource: model.BotTriggerSchedule,
			TriggerKey:    botScheduleTriggerKey(current.ID, planned),
			Status:        model.BotRunStatusRunning,
			SessionKind:   model.BotSessionKindSchedule,
			Summary:       "",
			// 密钥与 base_url 成对冻结：派发后用户换地址又换密钥时，
			// 不能把新密钥发往旧地址（运行结尾直接用这份密文）。
			APIKeyEncryptedSnapshot: append([]byte(nil), botRow.APIKeyEncrypted...),
		}
		taskID := current.ID
		run.ScheduleTaskID = &taskID
		run.ScheduledFor = &planned
		run.ConfigSnapshotJSON = marshalJSONString(snap)
		missed := now.Sub(planned) > botScheduleMissGrace
		if missed {
			// 错过的计划时间：跳过并记录，不执行、不补跑；直接落终态。
			run.Status = model.BotRunStatusSkipped
			run.Summary = "错过的计划时间，已跳过"
			finished := now
			run.FinishedAt = &finished
		} else {
			nowUTC := now
			run.StartedAt = &nowUTC
			run.TriggerContextJSON = marshalJSONString(map[string]interface{}{
				"task_id": current.ID, "scheduled_for": planned.UTC().Format(time.RFC3339Nano),
			})
		}
		if err := tx.Create(run).Error; err != nil {
			// 唯一索引冲突：同一计划时间已建过 run（重启/并发），不重复。
			if isBotRunDuplicateTriggerKey(err) {
				return nil
			}
			return err
		}
		if !missed {
			wakeRunID = run.ID
		}

		// CAS 推进 next_run_at 到严格晚于 now 的下一次；0 行 = 被并发抢走。
		_, next, err := parseAutomationCron(current.CronExpression, current.Timezone, now)
		if err != nil {
			// 配置损坏（如表达式被手工改库）：暂停调度并告警，不无限循环。
			s.logger.Error("定时任务 cron 解析失败，暂停调度",
				zap.String("task_id", current.ID), zap.Error(err))
			return repository.NewBotScheduleTaskRepository(tx).PauseTx(tx, current.ID)
		}
		_, err = repository.NewBotScheduleTaskRepository(tx).AdvanceNextRunTx(tx, current.ID, planned, next)
		return err
	})
	if txErr != nil {
		s.logger.Warn("处理到期定时任务失败", zap.String("task_id", task.ID), zap.Error(txErr))
		return
	}
	if wakeRunID != "" {
		s.wakeScheduleRun(task.BotID, wakeRunID)
	}
}

// botScheduleTriggerKey 计划时间幂等键：同一计划时间（含重启后重放）只建
// 一次 run。
func botScheduleTriggerKey(taskID string, planned time.Time) string {
	return "schedule:" + taskID + ":" + planned.UTC().Format(time.RFC3339Nano)
}

// isBotRunDuplicateTriggerKey 识别 (bot_id, trigger_key) 唯一冲突。
func isBotRunDuplicateTriggerKey(err error) bool {
	return err != nil && (errors.Is(err, gorm.ErrDuplicatedKey) ||
		strings.Contains(err.Error(), "UNIQUE constraint failed: bot_runs.bot_id, bot_runs.trigger_key"))
}

// ---------- 执行槽（D6） ----------

// wakeScheduleRun 把已存在的 schedule run 载入执行槽。同一 run 不重复执行：
// 槽占用即放弃（运行中循环会在安全点看到新输入）；run 已终态（如迟到结果）
// 不再续跑。
func (s *BotService) wakeScheduleRun(botID, runID string) {
	run, err := s.runs.FindByID(runID)
	if err != nil || run.BotID != botID || run.FinishedAt != nil ||
		run.SessionKind != model.BotSessionKindSchedule {
		return
	}
	slot := "schedule:" + runID
	if s.rt.acquire(slot, &botWake{Trigger: model.BotTriggerSchedule, Key: run.TriggerKey}) == nil {
		return
	}
	go s.runScheduleSlot(botID, runID)
}

// runScheduleSlot 是单个 schedule run 的执行槽：载入 run 进 botLoop 后立即
// 释放；schedule run 只执行一次，没有 requeue 循环。
func (s *BotService) runScheduleSlot(botID, runID string) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("定时任务执行 panic", zap.String("run_id", runID), zap.Any("panic", r))
			if run, err := s.runs.FindByID(runID); err == nil {
				if bot, err := s.bots.Get(botID); err == nil {
					s.finishFailed(bot, run, "panicked", errs.New(errs.ErrInternal.Code, "定时任务执行 panic"))
				}
			}
			s.rt.release("schedule:" + runID)
		}
	}()
	run, err := s.runs.FindByID(runID)
	if err != nil || run.FinishedAt != nil || run.SessionKind != model.BotSessionKindSchedule {
		s.rt.release("schedule:" + runID)
		return
	}
	bot, err := s.bots.Get(botID)
	if err != nil {
		s.rt.release("schedule:" + runID)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.rt.setCancel("schedule:"+runID, cancel)
	func() {
		defer cancel()
		s.botLoop(bot, run, ctx)
	}()
	s.rt.release("schedule:" + runID)
}

// ---------- schedule run 收尾（D3/D4：终态与事件同一事务） ----------

// tryFinishScheduleRun 完成前最后一次检查与 completed 落定在同一短事务；
// needs_report 且非静默时同事务追加运行事件（唤醒主线）。
func (s *BotService) tryFinishScheduleRun(bot *model.Bot, run *model.BotRun) bool {
	silent, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	finished := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		// 本次运行自己的异步写还没结算（queued/running）时不收尾：晚到的结果
		// 可能就是失败，是运行结果的一部分。等结算后循环会重建上下文。
		unsettled, err := repository.NewBotToolCallRepository(tx).CountUnsettledByRun(run.ID)
		if err != nil || unsettled > 0 {
			return err
		}
		hasNew, err := repository.NewBotMessageRepository(tx).HasModelInputSinceTx(
			tx, bot.ID, model.BotSessionKindSchedule, botInputRunID(run), run.InputThroughSeq)
		if err != nil || hasNew {
			return err
		}
		now := time.Now().UTC()
		ok, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":            model.BotRunStatusCompleted,
				"finished_at":       now,
				"input_through_seq": run.InputThroughSeq,
				"summary":           summary,
				"silent":            silent,
			})
		finished = ok
		if err != nil || !ok {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusCompleted),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusCompleted)},
		}); err != nil {
			return err
		}
		// 失败或「需要汇报且非静默」才追加事件；成功静默只记录。
		if !silent && run.ScheduleTaskID != nil {
			var task model.BotScheduleTask
			if err := tx.Where("id = ?", *run.ScheduleTaskID).First(&task).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil // 任务已删除：运行记录保留，事件无从谈起
				}
				return err
			}
			if task.NeedsReport {
				return s.appendScheduleEventTx(tx, bot.ID, &task, run,
					model.BotRunStatusCompleted, summary, "", "", now)
			}
		}
		return nil
	})
	if txErr != nil {
		s.logger.Warn("完成定时任务 run 失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return false
	}
	if finished {
		s.publishEvent(bot.ID)
	}
	return finished
}

// finishScheduleFailed 落 failed 终态并同事务追加运行事件（失败必报）。
func (s *BotService) finishScheduleFailed(bot *model.Bot, run *model.BotRun, code string, cause error) {
	now := time.Now().UTC()
	msg := botErrMessage(cause)
	_, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":        model.BotRunStatusFailed,
				"finished_at":   now,
				"error_code":    code,
				"error_message": msg,
				"summary":       summary,
			})
		if err != nil || !ok {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventError, botEventEnvelope{
			Kind: "run_error",
			Payload: map[string]interface{}{
				"code": code, "message": msg, "status": string(model.BotRunStatusFailed),
			},
		}); err != nil {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusFailed),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusFailed)},
		}); err != nil {
			return err
		}
		// 主线事件只在 run.ScheduleTaskID 可解析到任务时追加。
		if run.ScheduleTaskID == nil {
			return nil
		}
		var task model.BotScheduleTask
		if err := tx.Where("id = ?", *run.ScheduleTaskID).First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return s.appendScheduleEventTx(tx, bot.ID, &task, run,
			model.BotRunStatusFailed, summary, code, msg, now)
	})
	if txErr != nil {
		s.logger.Warn("落定时任务失败终态", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
		return
	}
	// CAS 失败：可能已被受理的停止抢占（错误检出后停止落库）。落 stopped，
	// 否则 run 会永远停在 running + stop_requested，无人再消费，还会让机器人
	// 删除被一直拒绝（与主线 finishFailed 同型兜底）。
	s.finishScheduleStopped(bot, run)
}

// finishScheduleStopped 落 stopped 终态：被取消的 schedule run 不追加主线
// 事件（用户主动停止不是需要汇报的运行结果）。
func (s *BotService) finishScheduleStopped(bot *model.Bot, run *model.BotRun) {
	now := time.Now().UTC()
	_, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil},
			map[string]interface{}{
				"status":      model.BotRunStatusStopped,
				"finished_at": now,
				"summary":     summary,
			})
		if err != nil || !ok {
			return err
		}
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusStopped),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusStopped)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落定时任务停止终态失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
	}
}

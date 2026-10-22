package service

import (
	"encoding/json"
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

// 启动恢复：迁移完成、所有新 worker 启动前执行。执行中崩溃一律失败、
// 不自动重放副作用；效果不确定时提示模型先读取核实，再决定新操作。
//
//  1. 未完成模型请求置 failed，活动主线 run 置 failed（server_restarted）。
//  2. running 工具调用置 failed；异步调用（已有回执）标记效果可能发生。
//  3. 对缺失协议回复的同步调用补「已被中断」回复；已有回执的只补终态事件。
//  4. pending_confirmation 保留；queued 保留（worker 启动后认领前重查授权）。
//  5. 恢复结果消息触发 recovery run；dedup_key 保证重复恢复不重复追加。
func (s *BotService) RecoverOnStartup() error {
	now := time.Now().UTC()

	// 1. 未完成的模型请求。
	requests, err := s.requests.ListUnfinished()
	if err != nil {
		return err
	}
	for _, req := range requests {
		_, _ = s.requests.UpdateColumns(req.ID, nil, map[string]interface{}{
			"status":        model.BotModelRequestFailed,
			"finished_at":   now,
			"error_code":    "server_restarted",
			"error_message": "服务在模型请求执行中重启，结果未知",
		})
	}

	// 1b. 维护运行与维护请求（INT-102）：执行中崩溃一律标 failed
	//（server_restarted），不自动重放；压缩周期的归档/摘要副作用以
	// operation_key 幂等键兜底，夜间整理由协调器下一日再触发。
	maintRepo := repository.NewBotMaintenanceRepository(s.db)
	if mRuns, mErr := maintRepo.ListUnfinished(); mErr == nil {
		for _, mr := range mRuns {
			_, _ = maintRepo.UpdateColumns(mr.ID,
				map[string]interface{}{"finished_at": nil},
				repository.MarkFinishedCols(model.BotMaintenanceFailed, now,
					"server_restarted", "服务在维护运行执行中重启，结果未知"))
			if mr.Kind == model.BotMaintenanceCompaction {
				s.recordInterruptedCompaction(mr, now)
			}
		}
	}
	if mReqs, mErr := maintRepo.ListUnfinishedRequests(); mErr == nil {
		for _, mreq := range mReqs {
			_, _ = maintRepo.UpdateRequestColumns(mreq.ID, nil, map[string]interface{}{
				"status":        model.BotModelRequestFailed,
				"finished_at":   now,
				"error_code":    "server_restarted",
				"error_message": "服务在维护请求执行中重启，结果未知",
			})
		}
	}

	// 2. 活动主线 run。
	runs, err := s.runs.ListActive()
	if err != nil {
		return err
	}

	// 3. running 调用：状态、协议回复/后台结果与事件在同一事务内补齐——
	// 中途再崩溃也不会留下「已失败但无回复」的协议缺口。
	calls, err := s.calls.ListUnfinished()
	if err != nil {
		return err
	}

	affectedBots := map[string]bool{}
	// 定时任务失败事件提交后不立即唤醒：恢复分四步（run 终态 → 调用终态 →
	// 协议回复 → 恢复通知），过早启动 goroutine 会让它还看到尚未恢复完的
	// running 调用、甚至复用一条还没被置 failed 的主线 run（INT-138 复审）。
	// 记事到全部恢复完成后再统一发出，仍不依赖 5s 扫描。
	var scheduleEventWakes []struct{ botID, runID string }
	for _, run := range runs {
		// 定时任务 run 中断也是失败：失败必报，运行事件必须与终态同一事务——
		// 分两次提交时进程若在中间退出，这条 run 已不再是活动 run，事件就永远
		// 补不回来了。任务与摘要都在事务外先读好：事务内复用同一连接再查询会
		// 自锁（MaxOpenConns=1）。
		var schedTask *model.BotScheduleTask
		schedSummary := ""
		if run.SessionKind == model.BotSessionKindSchedule && run.ScheduleTaskID != nil {
			task, err := s.schedTasks.FindByID(*run.ScheduleTaskID)
			if err == nil {
				schedTask = task
			} else {
				// 任务在崩溃前被删掉：失败仍是必报项，用派发时记下的任务名
				// 建 stub（与 finishScheduleFailed 对称），否则这次失败既报不
				// 出去、也无从经任务查询看到。
				schedTask = &model.BotScheduleTask{
					ID:   *run.ScheduleTaskID,
					Name: botScheduleTaskNameFromContext(&run),
				}
			}
			_, schedSummary = s.botSilentSummaryOf(run.BotID, run.ID)
		}
		appendedSchedEvent := false
		// 终态转移与 error/done 事件同一事务：一直挂在页面上的客户端重启
		// 后按 SSE 游标回放也能把原 run 折到 failed，不必等快照重读。
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			cols := map[string]interface{}{
				"status":        model.BotRunStatusFailed,
				"finished_at":   now,
				"error_code":    "server_restarted",
				"error_message": "服务在运行执行中重启，未完成步骤不再继续",
			}
			// 恢复收尾也要重算已计价成本：崩溃前已落库的失败/成功请求
			// 成本不能随 run 一起丢成 NULL（INT-139 云端复审）。
			s.runCostPersistColsTx(tx, run.ID, cols)
			ok, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
				map[string]interface{}{"finished_at": nil}, cols)
			if err != nil || !ok {
				return err
			}
			if err := s.appendEventTx(tx, run.BotID, runIDPtr(run.ID), "", model.BotEventError, botEventEnvelope{
				Kind: "run_error",
				Payload: map[string]interface{}{
					"code": "server_restarted", "message": "服务在运行执行中重启，未完成步骤不再继续",
					"status": string(model.BotRunStatusFailed),
				},
			}); err != nil {
				return err
			}
			if err := s.appendEventTx(tx, run.BotID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
				Kind: "run_done", Phase: string(model.BotRunStatusFailed),
				Payload: map[string]interface{}{"status": string(model.BotRunStatusFailed)},
			}); err != nil {
				return err
			}
			if schedTask == nil {
				return nil
			}
			if err := s.appendScheduleEventTx(tx, run.BotID, schedTask, &run,
				model.BotRunStatusFailed, schedSummary, "server_restarted",
				"服务在定时任务运行执行中重启，未完成步骤不再继续", now); err != nil {
				return err
			}
			appendedSchedEvent = true
			return nil
		})
		if txErr != nil {
			s.logger.Warn("恢复 run 失败", zap.String("run_id", run.ID), zap.Error(txErr))
			continue
		}
		affectedBots[run.BotID] = true
		s.publishEvent(run.BotID)
		if appendedSchedEvent {
			scheduleEventWakes = append(scheduleEventWakes,
				struct{ botID, runID string }{run.BotID, run.ID})
		}
	}

	for i := range calls {
		call := &calls[i]
		// running 调用：置 failed。异步调用副作用可能已发生。
		hadReceipt := s.callHasReceipt(call)
		isAsync := call.ExecutionMode == string(model.BotExecutionAsync)
		effect := isAsync && call.Status == model.BotCallStatusRunning
		interruptNote := "调用被服务重启中断；请先用读取类工具核实现状，再决定是否重试"
		if botIsMemoryWriteCall(call) {
			written, werr := repository.NewBotMemoryRepository(s.db).HasVersionWithOperationKey(call.BotID, call.ID)
			if werr == nil && written {
				effect = true
				interruptNote = "记忆写入已落库（已写入）；调用终态因重启未落定，请勿重复写入"
			} else {
				effect = true
				interruptNote = "记忆写入效果未知；请用 fs_memory.read 核实后再决定是否重试，不要自动重放"
			}
		}
		runID := s.runIDOfCall(call)
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			ok, err := repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
				string(model.BotCallStatusRunning), map[string]interface{}{
					"status":                   model.BotCallStatusFailed,
					"finished_at":              now,
					"error_code":               "server_restarted",
					"error_message":            "服务在调用执行中重启，结果未知",
					"effect_may_have_occurred": effect,
				})
			if err != nil || !ok {
				return err
			}
			call.Status = model.BotCallStatusFailed
			// 缺失协议回复（同步调用执行中）：补「已被中断」回复。
			if !hadReceipt {
				reply := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
					ToolName: call.ToolName, Status: string(model.BotCallStatusFailed),
					Content: botFailureReplyText(errs.New(errs.ErrInternal.Code, interruptNote)),
				}
				if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
					return err
				}
			}
			// 后台终态消息/事件（幂等；已有回执的异步调用在这里拿到终态投影）。
			return s.appendBackgroundResultTx(tx, runID, call, model.BotCallStatusFailed,
				botToolOutcome{ResultJSON: "{}", PreviewJSON: "{}", ResultChars: 2},
				errs.New(errs.ErrInternal.Code, "server_restarted"), effect, now)
		})
		if txErr != nil {
			s.logger.Warn("恢复调用失败", zap.String("call_id", call.ID), zap.Error(txErr))
			continue
		}
		affectedBots[call.BotID] = true
	}

	// 3b. queued 同步调用：提交后、执行前崩溃会留下永远无人认领的排队调用
	//（后台扫描只执行异步），模型上下文会带着未闭合的 tool_calls 块。恢复
	// 补 not_executed 终态与唯一回复，让模型重新决定。直接按同步调用分页
	// 取全——全局 queued 页面可能被异步调用挤占；条件更新挡住与活 worker
	// 认领的竞速，处理过的调用落终态后自然翻页。
	for {
		queued, err := s.calls.ListQueuedSync(256)
		if err != nil {
			return err
		}
		if len(queued) == 0 {
			break
		}
		for i := range queued {
			call := &queued[i]
			runID := s.runIDOfCall(call)
			txErr := s.db.Transaction(func(tx *gorm.DB) error {
				ok, err := repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
					string(model.BotCallStatusQueued), map[string]interface{}{
						"status":        model.BotCallStatusNotExecuted,
						"finished_at":   now,
						"error_code":    "not_executed",
						"error_message": "服务在调用执行前重启",
					})
				if err != nil || !ok {
					return err
				}
				call.Status = model.BotCallStatusNotExecuted
				reply := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
					ToolName: call.ToolName, Status: string(model.BotCallStatusNotExecuted),
					Content: botNotExecutedReplyText("server_restarted"),
				}
				if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
					return err
				}
				return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
					Kind: "tool_result", Phase: "terminal",
					Payload: botToolCallResponseOf(call),
				})
			})
			if txErr != nil {
				s.logger.Warn("恢复排队同步调用失败", zap.String("call_id", call.ID), zap.Error(txErr))
				continue
			}
			affectedBots[call.BotID] = true
		}
		if len(queued) < 256 {
			break
		}
	}

	// 4. 通知机器人：每个受影响 bot 追加 lifecycle 消息并触发 recovery run。
	for botID := range affectedBots {
		if err := s.appendRecoveryNotice(botID, now); err != nil {
			s.logger.Warn("追加恢复通知失败", zap.String("bot_id", botID), zap.Error(err))
			continue
		}
	}
	// 5. 全部恢复动作落库后再唤醒定时任务事件的主线（INT-138）：这里已经
	// 没有 running 调用、旧主线 run 均已失败，新 run 不会读到半恢复的账本。
	for _, wake := range scheduleEventWakes {
		s.wakeScheduleEvent(wake.botID, wake.runID, model.BotRunStatusFailed)
	}
	return nil
}

// callHasReceipt 检查调用是否已有协议回执（异步受理时追加）。
func (s *BotService) callHasReceipt(call *model.BotToolCall) bool {
	_, err := s.messages.FindByDedupKey(call.BotID, "receipt:"+call.ID)
	return err == nil
}

// appendRecoveryNotice 追加恢复通知（幂等）并发布事件；唤醒交给 Start 后的
// runtime（key 用通知消息 ID，重复恢复不会重复建 run）。
func (s *BotService) appendRecoveryNotice(botID string, now time.Time) error {
	var noticeID string
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		dedup := "recovery:" + now.UTC().Format("20060102-1504")
		if existing, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
			noticeID = existing.ID
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		seq, err := s.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		content := botLifecycleContent{
			Event: "recovered",
			Detail: json.RawMessage(`{"note":"服务重启：进行中的运行已中断并标记失败；` +
				`已受理的后台调用保留，效果不确定的操作请先核实再决定"}`),
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		noticeID = uuid.NewString()
		msg := &model.BotMessage{
			ID: noticeID, BotID: botID, Seq: seq,
			Kind: model.BotMessageKindLifecycle, ContentJSON: string(raw),
			DedupKey: &dedup, CreatedAt: now,
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return err
		}
		return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:    "lifecycle",
			Payload: map[string]interface{}{"event": "recovered", "message_id": noticeID},
		})
	})
	if txErr != nil {
		return txErr
	}
	s.publishEvent(botID)
	// recovery run 在 Start() 后由扫描/唤醒触发；这里直接唤醒亦可——runtime
	// 未启动时 wakeRuntime 会起 goroutine，循环首步 ensureRun 在无未消费输入
	// 时空转返回，安全。
	s.wakeRuntime(botID, botWake{
		Trigger: model.BotTriggerRecovery,
		Key:     "recovery:" + noticeID,
		Context: `{"notice_message_id":"` + noticeID + `"}`,
	})
	return nil
}

func (s *BotService) recordInterruptedCompaction(mr model.BotMaintenanceRun, now time.Time) {
	var n int64
	if err := s.db.Model(&model.BotCompaction{}).Where("maintenance_run_id = ?", mr.ID).Count(&n).Error; err != nil || n > 0 {
		return
	}
	code := "server_restarted"
	msg := "服务在维护运行执行中重启，结果未知"
	reason := model.BotCompactionThreshold
	if strings.Contains(mr.TriggerKey, string(model.BotCompactionForced)) {
		reason = model.BotCompactionForced
	} else if strings.Contains(mr.TriggerKey, string(model.BotCompactionStyle)) {
		reason = model.BotCompactionStyle
	}
	row := &model.BotCompaction{
		ID:                uuid.NewString(),
		BotID:             mr.BotID,
		MaintenanceRunID:  mr.ID,
		ParentRunID:       mr.ParentRunID,
		Reason:            reason,
		Status:            model.BotCompactionFailed,
		FoldedResultsJSON: "{}",
		Estimator:         botTokensEstimatorBytes35,
		ErrorCode:         &code,
		ErrorMessage:      &msg,
		CreatedAt:         now,
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		return repository.NewBotContextRepository(tx).CreateCompactionTx(tx, row)
	})
}

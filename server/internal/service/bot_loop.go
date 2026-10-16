package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 核心工具循环：只负责顺序（安全点 → 构建请求 → 流式 → 提交 → 执行/受理 →
// 收尾），不负责授权细节、数据库细节与 SSE 编码。循环体不追逻辑大全，
// 目标本文件保持接近 200 行的规模。

// botWake 是一次主线唤醒的触发描述。
type botWake struct {
	Trigger model.BotRunTrigger
	Key     string
	Context string
}

type botSafePoint struct {
	StopRequested bool
	HasNewInput   bool
}

// runBotSlot 之后的入口：确保 run 存在后进入循环。
func (s *BotService) runBot(botID string, wake botWake, ctx context.Context) {
	bot, err := s.bots.Get(botID)
	if err != nil {
		return
	}
	run, ok, err := s.ensureRun(bot, wake)
	if err != nil || !ok {
		return
	}
	s.botLoop(bot, run, ctx)
}

// botLoop 单 run 主循环。
func (s *BotService) botLoop(bot *model.Bot, run *model.BotRun, ctx context.Context) {
	key, err := s.decryptAPIKey(bot)
	if err != nil {
		s.finishFailed(bot, run, "key_decrypt_failed", err)
		return
	}
	client := botNewClient(runBaseURLOf(run, bot), key)
	streamClient := client

	var calls []*model.BotToolCall
	for {
		if input := s.safePoint(run); input.StopRequested {
			s.closeUnstartedSyncCalls(calls, "stop_requested")
			s.finishStopped(bot, run)
			return
		}

		built, err := s.buildRunContext(bot, run)
		if err != nil {
			s.finishFailed(bot, run, "context_build_failed", err)
			return
		}
		requestRow, err := s.beginModelRequest(bot, run, built)
		if err != nil {
			s.finishFailed(bot, run, "request_record_failed", err)
			return
		}
		s.advanceInputWatermarks(run, built.InputThroughSeq)

		// 预分配 assistant 消息 ID：流式增量信封先带上它，提交时复用同一个。
		assistantMsgID := uuid.NewString()
		deltas := s.newDeltaBuffer(bot.ID, run.ID, assistantMsgID)
		reply, streamErr := botStreamChat(ctx, &streamClient, built.Request, deltas.onDelta)
		deltas.flush()
		if streamErr != nil {
			// 失败（含用户停止打断）：草稿等不来 message_commit，丢弃。
			deltas.discard()
			s.finishModelRequest(requestRow, reply, streamErr)
			if input := s.safePoint(run); input.StopRequested {
				s.finishStopped(bot, run)
				return
			}
			s.finishFailed(bot, run, "model_request_failed", streamErr)
			return
		}
		s.finishModelRequest(requestRow, reply, nil)
		s.accumulateRunUsage(run, reply)

		calls, err := s.commitAssistantTurn(bot, run, requestRow, reply, assistantMsgID)
		if err != nil {
			s.finishFailed(bot, run, "commit_failed", err)
			return
		}

		if input := s.safePoint(run); input.StopRequested {
			s.closeUnstartedSyncCalls(calls, "stop_requested")
			s.finishStopped(bot, run)
			return
		}
		if len(reply.ToolCalls) == 0 {
			if input := s.safePoint(run); input.HasNewInput {
				continue
			}
			if s.tryFinishRun(bot, run) {
				return
			}
			continue
		}
		if input := s.safePoint(run); input.HasNewInput {
			// 有新输入：未开始的调用补 not_executed，让模型重新决定。
			s.closeUnstartedSyncCalls(calls, "new_input_before_execution")
			continue
		}

		for _, call := range calls {
			s.invokeOrAccept(bot, run, call, ctx)
			if input := s.safePoint(run); input.StopRequested || input.HasNewInput {
				s.closeUnstartedSyncCalls(calls, stopOrNewInputReason(input))
				break
			}
		}
	}
}

func stopOrNewInputReason(input botSafePoint) string {
	if input.StopRequested {
		return "stop_requested"
	}
	return "new_input_before_execution"
}

// runBaseURLOf run 期间的地址：优先配置快照。
func runBaseURLOf(run *model.BotRun, bot *model.Bot) string {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err == nil && snap.BaseURL != "" {
		return snap.BaseURL
	}
	return bot.BaseURL
}

// safePoint 读取停止标记与未消费输入；run 的本地水位同步自数据库。
func (s *BotService) safePoint(run *model.BotRun) botSafePoint {
	input := botSafePoint{}
	fresh, err := s.runs.FindByID(run.ID)
	if err == nil {
		if fresh.InputThroughSeq > run.InputThroughSeq {
			run.InputThroughSeq = fresh.InputThroughSeq
		}
		input.StopRequested = fresh.StopRequestedAt != nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.Warn("读取 run 失败", zap.String("run_id", run.ID), zap.Error(err))
	}
	hasNew, err := s.messages.HasModelInputSince(run.BotID, run.InputThroughSeq)
	if err == nil {
		input.HasNewInput = hasNew
	}
	return input
}

// advanceInputWatermarks 推进 run 消费水位与 bot 的 offered 水位（已交给模型
// 请求的输入不再自动重试）。
func (s *BotService) advanceInputWatermarks(run *model.BotRun, seq int64) {
	if seq <= run.InputThroughSeq {
		return
	}
	run.InputThroughSeq = seq
	_, _ = s.runs.UpdateColumns(run.ID, nil, map[string]interface{}{"input_through_seq": seq})
	_ = s.bots.SetOfferedInputSeq(run.BotID, seq)
}

// tryFinishRun 最后一次检查与 completed 落定在同一短事务：检查后恰好到达的
// 新消息不会被丢唤醒。
func (s *BotService) tryFinishRun(bot *model.Bot, run *model.BotRun) bool {
	finished := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		hasNew, err := repository.NewBotMessageRepository(tx).HasModelInputSinceTx(tx, bot.ID, run.InputThroughSeq)
		if err != nil || hasNew {
			return err
		}
		now := time.Now().UTC()
		// 完成转移必须带 stop_requested_at IS NULL：停止请求落在本事务前
		// 一刻也不能被 completed 覆盖，返回 false 后由下一轮安全点落
		// stopped。
		ok, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":            model.BotRunStatusCompleted,
				"finished_at":       now,
				"input_through_seq": run.InputThroughSeq,
			})
		finished = ok
		if err != nil || !ok {
			return err
		}
		// 终态事件与终态转移同一事务：崩溃不会留下没有 done 事件的已终
		// 态 run（重连客户端按游标回放时 run 栏会永远卡在 running）。
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusCompleted),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusCompleted)},
		})
	})
	if txErr != nil {
		s.logger.Warn("完成 run 失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return false
	}
	if finished {
		s.publishEvent(bot.ID)
	}
	return finished
}

// finishStopped / finishFailed 落定终态并发 done（失败先 error 再 done）。
// 终态与事件同一事务：崩溃不留无事件的已终态 run；提交后再发布 SSE。
func (s *BotService) finishStopped(bot *model.Bot, run *model.BotRun) {
	now := time.Now().UTC()
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil},
			map[string]interface{}{"status": model.BotRunStatusStopped, "finished_at": now})
		if err != nil || !ok {
			return err
		}
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusStopped),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusStopped)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落停止终态失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
	}
}

func (s *BotService) finishFailed(bot *model.Bot, run *model.BotRun, code string, cause error) {
	now := time.Now().UTC()
	msg := botErrMessage(cause)
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil},
			map[string]interface{}{
				"status":        model.BotRunStatusFailed,
				"finished_at":   now,
				"error_code":    code,
				"error_message": msg,
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
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusFailed),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusFailed)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落失败终态", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
	}
}

// ---------- 模型请求记录 ----------

func (s *BotService) beginModelRequest(bot *model.Bot, run *model.BotRun, built *botBuiltContext) (*model.BotModelRequest, error) {
	var row *model.BotModelRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ordinal, err := repository.NewBotModelRequestRepository(tx).NextOrdinalTx(tx, run.ID)
		if err != nil {
			return err
		}
		row = &model.BotModelRequest{
			ID:                  uuid.NewString(),
			RunID:               run.ID,
			Ordinal:             ordinal,
			Status:              model.BotModelRequestRunning,
			InputThroughSeq:     built.InputThroughSeq,
			PromptRevision:      bot.PromptRevision,
			ToolsetVersion:      bot.ToolsetVersion,
			PrefixHash:          built.PrefixHash,
			ToolsHash:           built.ToolsHash,
			RequestHash:         built.RequestHash,
			ToolsSchemaBytes:    built.ToolsSchemaBytes,
			ToolsTokensEstimate: built.ToolsTokensEstimate,
			TokenEstimator:      built.TokenEstimator,
			StartedAt:           time.Now().UTC(),
		}
		return repository.NewBotModelRequestRepository(tx).CreateTx(tx, row)
	})
	return row, err
}

// finishModelRequest 落请求终态与 usage；usage 缺失与零严格区分。
func (s *BotService) finishModelRequest(row *model.BotModelRequest, reply *botModelReply, streamErr error) {
	cols := map[string]interface{}{"finished_at": time.Now().UTC()}
	if streamErr != nil {
		cols["status"] = model.BotModelRequestFailed
		cols["error_code"] = "model_request_failed"
		cols["error_message"] = botErrMessage(streamErr)
	} else {
		cols["status"] = model.BotModelRequestSucceeded
		if row != nil && reply != nil {
			cols["provider_request_id"] = reply.ProviderRequestID
			if reply.UsageComplete {
				cols["prompt_tokens"] = reply.PromptTokens
				cols["completion_tokens"] = reply.CompletionTokens
				cols["total_tokens"] = reply.TotalTokens
				cols["cached_tokens"] = reply.CachedTokens
			}
		}
	}
	if row == nil {
		return
	}
	if _, err := s.requests.UpdateColumns(row.ID, nil, cols); err != nil {
		s.logger.Warn("落模型请求终态失败", zap.String("request_id", row.ID), zap.Error(err))
	}
}

// accumulateRunUsage 把本次请求的 usage 累加进 run（内存累计，收尾统一落库）。
func (s *BotService) accumulateRunUsage(run *model.BotRun, reply *botModelReply) {
	if reply == nil || !reply.UsageComplete {
		return
	}
	run.PromptTokens = addToPtr(run.PromptTokens, reply.PromptTokens)
	run.CompletionTokens = addToPtr(run.CompletionTokens, reply.CompletionTokens)
	run.TotalTokens = addToPtr(run.TotalTokens, reply.TotalTokens)
	run.CachedTokens = addToPtr(run.CachedTokens, reply.CachedTokens)
	run.UsageComplete = true
}

func addToPtr(p *int64, v int64) *int64 {
	if p == nil {
		out := v
		return &out
	}
	*p += v
	return p
}

// ---------- 流式增量批量落事件 ----------

// botDeltaBuffer 合并正文流式片段（约 100ms 一批写入），完整 assistant 消息
// 仍只在模型调用结束时追加一次；流式片段不改消息。msgID 是预分配的
// assistant 消息 ID：增量信封带上它，客户端才能把片段挂到正确草稿上。
type botDeltaBuffer struct {
	svc   *BotService
	botID string
	runID string
	msgID string

	mu    sync.Mutex
	buf   strings.Builder
	timer *time.Timer
}

func (s *BotService) newDeltaBuffer(botID, runID, msgID string) *botDeltaBuffer {
	return &botDeltaBuffer{svc: s, botID: botID, runID: runID, msgID: msgID}
}

func (d *botDeltaBuffer) onDelta(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf.WriteString(text)
	if d.timer == nil {
		d.timer = time.AfterFunc(100*time.Millisecond, d.flush)
	}
}

// flush 取走当前缓冲整体落一次事件；定时器与流末尾并发调用时由互斥与
// 空缓冲判空保证不重复。写完即复位，后续增量继续按 100ms 批量落库。
func (d *botDeltaBuffer) flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	text := d.buf.String()
	d.buf.Reset()
	if text == "" {
		return
	}
	runID := d.runID
	err := d.svc.db.Transaction(func(tx *gorm.DB) error {
		return d.svc.appendEventTx(tx, d.botID, &runID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "text_delta",
			MessageID: d.msgID,
			Payload:   map[string]interface{}{"text": text, "run_id": d.runID},
		})
	})
	if err != nil {
		d.svc.logger.Warn("写入流式事件失败", zap.String("bot_id", d.botID), zap.Error(err))
		return
	}
	d.svc.publishEvent(d.botID)
}

// discard 请求客户端丢弃已挂出的部分草稿：模型请求失败（含用户停止打断）
// 时草稿不会等来 message_commit，不丢弃会被当成完整回复一直挂着。
func (d *botDeltaBuffer) discard() {
	runID := d.runID
	err := d.svc.db.Transaction(func(tx *gorm.DB) error {
		return d.svc.appendEventTx(tx, d.botID, &runID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "message_discard",
			MessageID: d.msgID,
		})
	})
	if err != nil {
		d.svc.logger.Warn("写入草稿丢弃事件失败", zap.String("bot_id", d.botID), zap.Error(err))
		return
	}
	d.svc.publishEvent(d.botID)
}

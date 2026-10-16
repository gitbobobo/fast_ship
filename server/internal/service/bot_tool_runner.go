package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 工具执行：assistant 回合提交、同步执行、异步 worker、取消与终态落定。
// 调用记录先落库再执行；CAS 认领与条件终态保证同一 call 只有一次执行、
// 一个终态；回执与 SSE 事件提交后才允许 worker 被唤醒。

const botWorkerCount = 4
const botAsyncExecTimeout = 15 * time.Minute

// ---------- assistant 回合提交 ----------

// commitAssistantTurn 在一个短事务内提交：assistant 消息、每调用的账本行、
// 异步回执（dedup receipt:<call_id>）、校验失败的即时回复与全部 SSE 事件。
// scope/授权解析与调用 ID 分配都在事务外完成（消息账本有防 UPDATE 守护，
// assistant 行必须一次写对）；提交成功后才唤醒 worker。msgID 由调用方在
// 模型请求前预分配，流式增量信封已携带同一 ID。
func (s *BotService) commitAssistantTurn(bot *model.Bot, run *model.BotRun, requestRow *model.BotModelRequest, reply *botModelReply, msgID string) ([]*model.BotToolCall, error) {
	now := time.Now().UTC()
	requestID := requestRow.ID

	// 事务外：逐调用分配 ID、解析/校验/解析范围/匹配授权。
	type preparedEntry struct {
		call       *model.BotToolCall
		providerID string
		toolName   string
		prepared   *botPreparedCall
		execCtx    botCallExecContext
		authzID    *string
		invalidErr error
	}
	entries := make([]preparedEntry, 0, len(reply.ToolCalls))
	content := botAssistantContent{Text: reply.Text}
	for _, tc := range reply.ToolCalls {
		entry := preparedEntry{
			providerID: tc.ID,
			toolName:   tc.Name,
			call: &model.BotToolCall{
				ID:                 uuid.NewString(),
				BotID:              bot.ID,
				RequestID:          requestID,
				ProviderToolCallID: tc.ID,
				Ordinal:            int64(len(entries)),
				CreatedAt:          now,
			},
		}
		prepared, err := botPrepareToolCall(tc.Name, tc.Arguments)
		if err != nil {
			entry.invalidErr = err
			entry.call.ToolName = tc.Name
			entry.call.Status = model.BotCallStatusFailed
			entry.call.FinishedAt = &now
			entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
			entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
		} else {
			entry.prepared = prepared
			s.resolveCallScope(bot, prepared, tc.Arguments, &entry.execCtx)
			if prepared.RequiresConfirmation {
				if err := botRequireScope(prepared); err != nil {
					entry.invalidErr = err
					entry.call.Status = model.BotCallStatusFailed
					entry.call.FinishedAt = &now
					entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
					entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
					entry.prepared = nil
				} else if authz := s.matchAuthorization(bot.ID, prepared); authz != nil {
					id := authz.ID
					entry.authzID = &id
				}
			}
		}
		if entry.prepared != nil {
			call := entry.call
			call.ToolName = entry.prepared.ToolName
			call.Action = entry.prepared.Action
			call.ArgumentsJSON = entry.prepared.ArgumentsJSON
			call.ArgumentsHash = entry.prepared.ArgumentsHash
			call.ExecutionMode = string(entry.prepared.Mode)
			call.RequiresConfirmation = entry.prepared.RequiresConfirmation
			call.ExecutionContextJSON = marshalExecContext(entry.execCtx)
			switch {
			case entry.prepared.Mode == model.BotExecutionSync:
				call.Status = model.BotCallStatusQueued
			case entry.authzID != nil:
				call.Status = model.BotCallStatusQueued
				call.AuthorizationID = entry.authzID
			default:
				call.Status = model.BotCallStatusPendingConfirmation
			}
		}
		content.ToolCalls = append(content.ToolCalls, botAssistantToolCall{
			CallID:             entry.call.ID,
			ProviderToolCallID: tc.ID,
			ToolName:           tc.Name,
			Action:             entry.call.Action,
			Arguments:          entry.call.ArgumentsJSON,
		})
		entries = append(entries, entry)
	}

	calls := make([]*model.BotToolCall, 0, len(entries))
	assistantRaw, _ := json.Marshal(content)

	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		txCalls := repository.NewBotToolCallRepository(tx)

		// 1. assistant 消息（与回执连续落库，协议块完整；一次写对）。
		seq, err := s.bots.NextMessageSeqTx(tx, bot.ID)
		if err != nil {
			return err
		}
		assistantMsg := &model.BotMessage{
			ID: msgID, BotID: bot.ID, Seq: seq,
			RunID: &run.ID, RequestID: &requestID,
			Kind: model.BotMessageKindAssistant, ContentJSON: string(assistantRaw),
			CreatedAt: now,
		}
		if err := txMsgs.AppendTx(tx, assistantMsg); err != nil {
			return err
		}

		// 2. 调用账本行 + 回执 / 失败回复 / 事件。
		for i := range entries {
			entry := &entries[i]
			call := entry.call
			if err := txCalls.CreateTx(tx, call); err != nil {
				return err
			}
			calls = append(calls, call)

			// 事件：tool_call 提案，载荷带完整调用投影（参数、执行模式、
			// 确认要求）——确认卡不靠前端猜默认值。
			phase := "proposed"
			if call.Status == model.BotCallStatusPendingConfirmation {
				phase = "awaiting_confirmation"
			} else if call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync) {
				phase = "queued"
			}
			if err := s.appendEventTx(tx, bot.ID, &run.ID, call.ID, model.BotEventToolCall, botEventEnvelope{
				Kind: "tool_call", Phase: phase,
				Payload: botToolCallResponseOf(call),
			}); err != nil {
				return err
			}

			switch {
			case entry.invalidErr != nil:
				// 协议要求每个 provider_tool_call_id 恰好一条回复：校验失败
				// 也即时补齐失败回复。
				replyContent := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: entry.providerID,
					ToolName: entry.toolName, Status: string(model.BotCallStatusFailed),
					Content: botFailureReplyText(entry.invalidErr),
				}
				if err := s.appendToolReplyTx(tx, bot.ID, run.ID, requestID, call, replyContent, now); err != nil {
					return err
				}
			case call.ExecutionMode == string(model.BotExecutionAsync):
				// 异步回执：accepted=true 表示受理，不代表已执行。
				status := string(model.BotCallStatusPendingConfirmation)
				if call.Status == model.BotCallStatusQueued {
					status = string(model.BotCallStatusQueued)
				}
				receiptRaw, _ := json.Marshal(map[string]interface{}{
					"accepted": true, "call_id": call.ID, "status": status,
					"tool_name": call.ToolName, "action": call.Action,
					"requires_confirmation": call.RequiresConfirmation,
				})
				replyContent := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: entry.providerID,
					ToolName: call.ToolName, Status: status,
					Content: string(receiptRaw),
				}
				if err := s.appendToolReplyTx(tx, bot.ID, run.ID, requestID, call, replyContent, now); err != nil {
					return err
				}
			}
		}

		// 3. message_commit 事件（供其他标签页同步完整消息）。
		if err := s.appendEventTx(tx, bot.ID, &run.ID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "message_commit",
			MessageID: assistantMsg.ID,
			Payload:   botMessageResponseOf(assistantMsg),
		}); err != nil {
			return err
		}

		// 4. run usage 列（内存累计值落库）。
		usageCols := map[string]interface{}{}
		if run.PromptTokens != nil {
			usageCols["prompt_tokens"] = *run.PromptTokens
		}
		if run.CompletionTokens != nil {
			usageCols["completion_tokens"] = *run.CompletionTokens
		}
		if run.TotalTokens != nil {
			usageCols["total_tokens"] = *run.TotalTokens
		}
		if run.CachedTokens != nil {
			usageCols["cached_tokens"] = *run.CachedTokens
		}
		if run.UsageComplete {
			usageCols["usage_complete"] = true
		}
		if len(usageCols) > 0 {
			if _, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID, nil, usageCols); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	s.publishEvent(bot.ID)

	// 事务提交后才唤醒 worker（回执已可见）。
	for _, call := range calls {
		if call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync) {
			s.enqueueCallWork(call.ID)
		}
	}
	return calls, nil
}

// resolveCallScope 在受理时解析写操作范围并填充执行上下文；读操作不设范围。
func (s *BotService) resolveCallScope(bot *model.Bot, prepared *botPreparedCall, rawArguments string, execCtx *botCallExecContext) {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rawArguments), &raw)
	execCtx.ScopeKind = prepared.ScopeKind
	execCtx.ScopeID = prepared.ScopeID
	execCtx.Fields = prepared.FieldsConstraint

	switch prepared.ToolName {
	case "fs_project":
		s.resolveProjectScope(bot, prepared, raw)
	case "fs_issue":
		s.resolveIssueScope(bot, prepared, raw)
	case "fs_thread":
		s.resolveThreadScope(prepared, raw)
		threadID, commandID, messageID := buildThreadExecIDs(prepared.Action)
		execCtx.ThreadID, execCtx.CommandID, execCtx.MessageID = threadID, commandID, messageID
		if prepared.EnvironmentID != nil {
			execCtx.EnvID = *prepared.EnvironmentID
		}
	}
	// scope 解析可能回填（issue→project），同步回执行上下文。
	execCtx.ScopeKind = prepared.ScopeKind
	execCtx.ScopeID = prepared.ScopeID
	execCtx.Fields = prepared.FieldsConstraint
}

// botRequireScope 写操作必须有可展示的具体范围，否则建卡失败。
func botRequireScope(prepared *botPreparedCall) error {
	if prepared.ScopeKind == "" || prepared.ScopeID == "" {
		return errs.New(errs.ErrInvalidParams.Code, "无法确定该操作的目标（参数缺失或目标不存在）")
	}
	return nil
}

func marshalExecContext(ctx botCallExecContext) string {
	raw, _ := json.Marshal(ctx)
	return string(raw)
}

// botFailureReplyText 面向模型的失败回复正文。
func botFailureReplyText(err error) string {
	payload, _ := json.Marshal(map[string]interface{}{
		"accepted": false,
		"error":    botErrMessage(err),
	})
	return string(payload)
}

// botErrCodeOf 把 AppError 的业务码折算成稳定错误码字符串。
func botErrCodeOf(err error) string {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return fmt.Sprintf("e%d", appErr.Code)
	}
	return "invalid_arguments"
}

// appendToolReplyTx 追加唯一的 tool 角色回复（dedup terminal/receipt）。
func (s *BotService) appendToolReplyTx(tx *gorm.DB, botID, runID, requestID string, call *model.BotToolCall, content botToolReplyContent, now time.Time) error {
	txMsgs := repository.NewBotMessageRepository(tx)
	dedup := "receipt:" + call.ID
	if call.Status.IsTerminal() {
		dedup = "terminal:" + call.ID
	}
	if existing, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
		_ = existing
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return err
	}
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		RunID: &runID, RequestID: &requestID, CallID: &call.ID,
		Kind: model.BotMessageKindToolReply, ContentJSON: string(raw),
		DedupKey: &dedup, CreatedAt: now,
	}
	return txMsgs.AppendTx(tx, msg)
}

// ---------- 同步执行（主线循环内） ----------

// invokeOrAccept 单个调用的执行入口：同步认领执行，异步交给 worker。
func (s *BotService) invokeOrAccept(bot *model.Bot, run *model.BotRun, call *model.BotToolCall, ctx context.Context) {
	switch {
	case call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionSync):
		s.executeSyncCall(bot, run.ID, call, ctx)
	case call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync):
		s.enqueueCallWork(call.ID)
	}
}

// executeSyncCall 认领并执行同步工具，落终态与协议回复。
func (s *BotService) executeSyncCall(bot *model.Bot, runID string, call *model.BotToolCall, ctx context.Context) {
	now := time.Now().UTC()
	var claimed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		claimed, err = repository.NewBotToolCallRepository(tx).ClaimQueuedTx(tx, call.ID, now)
		return err
	})
	if err != nil || !claimed {
		return // 已被取消或被 worker 认领
	}
	call.Status = model.BotCallStatusRunning
	call.StartedAt = &now

	content, execErr := s.botExecuteSyncTool(ctx, call)
	outcome := botBuildOutcomeRaw(call.ID, content, execErr)
	status := model.BotCallStatusSucceeded
	if execErr != nil {
		status = model.BotCallStatusFailed
	}
	s.finalizeCall(bot, runID, call, status, outcome, execErr, false)
}

// closeUnstartedSyncCalls 给尚未认领的同步调用补 not_executed 回复，让模型
// 重新决定；已受理的后台调用保持原状态。
func (s *BotService) closeUnstartedSyncCalls(calls []*model.BotToolCall, reason string) {
	for _, call := range calls {
		if call.Status != model.BotCallStatusQueued || call.ExecutionMode != string(model.BotExecutionSync) {
			continue
		}
		runID := s.runIDOfCall(call)
		now := time.Now().UTC()
		ok, err := s.calls.UpdateWhere(call.ID, string(model.BotCallStatusQueued), map[string]interface{}{
			"status":        model.BotCallStatusNotExecuted,
			"finished_at":   now,
			"error_code":    "not_executed",
			"error_message": reason,
		})
		if err != nil || !ok {
			continue
		}
		call.Status = model.BotCallStatusNotExecuted
		reply := botToolReplyContent{
			CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
			ToolName: call.ToolName, Status: string(model.BotCallStatusNotExecuted),
			Content: botFailureReplyText(errs.New(errs.ErrInvalidParams.Code,
				"调用未执行（"+reason+"）：请在新输入到达后重新决定是否再次调用")),
		}
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			return s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now)
		})
		if txErr != nil {
			s.logger.Warn("补 not_executed 回复失败", zap.String("call_id", call.ID), zap.Error(txErr))
			continue
		}
		s.emitEvent(call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
			Kind: "tool_result", Phase: "terminal",
			Payload: map[string]interface{}{"status": string(model.BotCallStatusNotExecuted), "reason": reason},
		})
	}
}

// runIDOfCall 从请求记录反查 run（事务外调用）。
func (s *BotService) runIDOfCall(call *model.BotToolCall) string {
	req, err := s.requests.FindByID(call.RequestID)
	if err != nil {
		return ""
	}
	return req.RunID
}

// ---------- 后台 worker ----------

func (s *BotService) startWorkers() {
	s.workCh = make(chan string, 256)
	s.callCancels = map[string]context.CancelFunc{}
	for i := 0; i < botWorkerCount; i++ {
		go s.workerLoop()
	}
}

func (s *BotService) workerLoop() {
	for callID := range s.workCh {
		s.executeQueuedCall(callID)
	}
}

func (s *BotService) enqueueCallWork(callID string) {
	s.ensureWorkers()
	select {
	case s.workCh <- callID:
	default:
		// 队列满：扫描循环会补推（丢失推送不丢失任务，数据库是事实来源）。
	}
}

func (s *BotService) ensureWorkers() {
	s.workOnce.Do(func() { s.startWorkers() })
}

// scanQueuedCalls 推送排队的异步调用；重复推送无害（CAS 认领挡并发）。
func (s *BotService) scanQueuedCalls() {
	calls, err := s.calls.ListQueued(64)
	if err != nil {
		return
	}
	for _, call := range calls {
		if call.ExecutionMode == string(model.BotExecutionAsync) {
			s.enqueueCallWork(call.ID)
		}
	}
}

// executeQueuedCall 认领前重新核对归属与授权；执行、落终态、追加结果消息
// 并唤醒主线。
func (s *BotService) executeQueuedCall(callID string) {
	call, err := s.calls.FindByID(callID)
	if err != nil || call.Status != model.BotCallStatusQueued {
		return
	}
	bot, err := s.bots.Get(call.BotID)
	if err != nil {
		return
	}

	// 撤销对尚未认领的授权调用生效：授权失效则回到待确认（快速路径）。
	if call.AuthorizationID != nil {
		authz, authzErr := s.authz.FindByID(*call.AuthorizationID)
		if authzErr != nil || authz.RevokedAt != nil || authz.BotID != bot.ID {
			s.revertQueuedCall(call)
			return
		}
	}

	// 认领与授权复核在同一事务内：撤销与认领的竞速由事务串行化裁决，
	// 已撤销的授权不能再把调用推进到 running。
	now := time.Now().UTC()
	var claimed, authzRevoked bool
	claimErr := s.db.Transaction(func(tx *gorm.DB) error {
		claimed, authzRevoked = false, false
		if call.AuthorizationID != nil {
			authz, err := repository.NewBotAuthorizationRepository(tx).FindByID(*call.AuthorizationID)
			if err != nil {
				return err
			}
			if authz.RevokedAt != nil || authz.BotID != bot.ID {
				authzRevoked = true
				return nil
			}
		}
		var err error
		claimed, err = repository.NewBotToolCallRepository(tx).ClaimQueuedTx(tx, callID, now)
		return err
	})
	if claimErr != nil {
		return
	}
	if authzRevoked || !claimed {
		if authzRevoked {
			s.revertQueuedCall(call)
		}
		return
	}
	call.Status = model.BotCallStatusRunning
	call.StartedAt = &now
	s.emitEvent(bot.ID, nil, call.ID, model.BotEventToolCall, botEventEnvelope{
		Kind: "tool_call", Phase: "running",
		Payload: botToolCallResponseOf(call),
	})

	ctx, cancel := context.WithTimeout(context.Background(), botAsyncExecTimeout)
	s.registerCallCancel(callID, cancel)
	defer func() {
		cancel()
		s.unregisterCallCancel(callID)
	}()

	output, execErr := s.runAsyncTool(ctx, call)

	status := model.BotCallStatusSucceeded
	if execErr != nil {
		status = model.BotCallStatusFailed
		// 取消竞速：取消请求已记录则终态为 cancelled，且副作用可能已发生。
		fresh, ferr := s.calls.FindByID(callID)
		if ferr == nil && fresh.CancelRequestedAt != nil {
			status = model.BotCallStatusCancelled
		}
	}
	effect := status == model.BotCallStatusCancelled
	outcome := botBuildOutcome(call.ID, output, execErr)
	s.finalizeCall(bot, s.runIDOfCall(call), call, status, outcome, execErr, effect)
}

// runAsyncTool 分派异步写操作到对应适配器。
func (s *BotService) runAsyncTool(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	switch call.ToolName {
	case "fs_project":
		return s.runProjectWrite(ctx, call)
	case "fs_issue":
		return s.runIssueWrite(ctx, call)
	case "fs_thread":
		return s.runThreadWrite(ctx, call)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "工具没有异步实现: "+call.ToolName)
}

// finalizeCall 原子落终态、结果、background_result 消息与事件；CAS 失败说明
// 终态已被别人落定（取消竞速），迟到返回不覆盖。
func (s *BotService) finalizeCall(bot *model.Bot, runID string, call *model.BotToolCall, status model.BotToolCallStatus, outcome botToolOutcome, execErr error, effect bool) {
	now := time.Now().UTC()
	cols := map[string]interface{}{
		"status":                   status,
		"finished_at":              now,
		"result_json":              outcome.ResultJSON,
		"result_preview_json":      outcome.PreviewJSON,
		"result_chars":             outcome.ResultChars,
		"truncated":                outcome.Truncated,
		"effect_may_have_occurred": effect,
	}
	if execErr != nil {
		cols["error_code"] = botErrCodeOf(execErr)
		cols["error_message"] = botErrMessage(execErr)
	}
	var ok bool
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
			string(model.BotCallStatusRunning), cols)
		if err != nil || !ok {
			return err
		}
		if call.ExecutionMode != string(model.BotExecutionAsync) {
			// 同步工具：协议回复就在本事务内补齐（每个 provider_tool_call_id
			// 恰好一条 tool 消息）；不追加第二条结果消息。tool_result 事件
			// 同事务落库：其他标签页的卡片不靠重读也能翻到终态。
			call.Status = status
			content := outcome.PreviewJSON
			if execErr != nil {
				content = botFailureReplyText(execErr)
			}
			reply := botToolReplyContent{
				CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
				ToolName: call.ToolName, Status: string(status),
				Content: content,
			}
			if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
				return err
			}
			return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
				Kind: "tool_result", Phase: "terminal",
				Payload: botToolCallResponseOf(call),
			})
		}
		// 平台后台结果只属于异步调用（完成事件不复用 provider_tool_call_id）。
		return s.appendBackgroundResultTx(tx, runID, call, status, outcome, execErr, effect, now)
	})
	if txErr != nil {
		s.logger.Warn("落调用终态失败", zap.String("call_id", call.ID), zap.Error(txErr))
		return
	}
	if !ok {
		return // 终态已被取消路径落定
	}
	s.publishEvent(bot.ID)
	// 新结果唤醒现有 run，或触发新的 tool_result run。
	s.wakeRuntime(bot.ID, botWake{
		Trigger: model.BotTriggerToolResult,
		Key:     "tool_result:" + call.ID,
		Context: marshalJSONString(map[string]interface{}{"call_id": call.ID, "status": string(status)}),
	})
}

// botResultSummary 从完整结果中截取一行摘要供后台结果消息展示。
func botResultSummary(resultJSON string) string {
	runes := []rune(resultJSON)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func runIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func marshalJSONString(v interface{}) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// ---------- 取消 ----------

func (s *BotService) registerCallCancel(callID string, cancel context.CancelFunc) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.callCancels == nil {
		s.callCancels = map[string]context.CancelFunc{}
	}
	s.callCancels[callID] = cancel
}

func (s *BotService) unregisterCallCancel(callID string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.callCancels, callID)
}

func (s *BotService) signalCallCancel(callID string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if cancel, ok := s.callCancels[callID]; ok {
		cancel()
	}
}

// cancelBotCall 取消本机器人的后台调用：待确认/queued 直接 CAS 终结；
// running 记录取消并取消其 context（worker 落唯一终态）。返回最新调用与
// 是否发生了取消动作。
func (s *BotService) cancelBotCall(call *model.BotToolCall) (*model.BotToolCall, bool) {
	now := time.Now().UTC()
	switch call.Status {
	case model.BotCallStatusPendingConfirmation, model.BotCallStatusQueued:
		ok, err := s.calls.UpdateWhere(call.ID, string(call.Status), map[string]interface{}{
			"status":        model.BotCallStatusCancelled,
			"finished_at":   now,
			"error_code":    "cancelled",
			"error_message": "被用户或机器人取消（未执行）",
		})
		if err != nil || !ok {
			fresh, _ := s.calls.FindByID(call.ID)
			return fresh, false
		}
		// 终态结果消息（唯一 terminal）。
		runID := s.runIDOfCall(call)
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			return s.appendBackgroundResultTx(tx, runID, call, model.BotCallStatusCancelled, botToolOutcome{}, nil, false, now)
		})
		if txErr != nil {
			s.logger.Warn("追加取消结果失败", zap.String("call_id", call.ID), zap.Error(txErr))
		}
		s.emitEvent(call.BotID, nil, call.ID, model.BotEventToolResult, botEventEnvelope{
			Kind: "tool_result", Phase: "terminal",
			Payload: map[string]interface{}{"status": string(model.BotCallStatusCancelled)},
		})
		s.publishEvent(call.BotID)
		fresh, _ := s.calls.FindByID(call.ID)
		return fresh, true
	case model.BotCallStatusRunning:
		_, err := s.calls.UpdateWhere(call.ID, string(model.BotCallStatusRunning), map[string]interface{}{
			"cancel_requested_at": now,
		})
		if err != nil {
			fresh, _ := s.calls.FindByID(call.ID)
			return fresh, false
		}
		s.signalCallCancel(call.ID)
		fresh, _ := s.calls.FindByID(call.ID)
		return fresh, true
	default:
		return call, false
	}
}

// appendBackgroundResultTx 在事务内追加平台后台结果消息与事件（幂等）。
func (s *BotService) appendBackgroundResultTx(tx *gorm.DB, runID string, call *model.BotToolCall, status model.BotToolCallStatus, outcome botToolOutcome, execErr error, effect bool, now time.Time) error {
	result := botBackgroundResultContent{
		CallID: call.ID, ToolName: call.ToolName, Action: call.Action,
		Status: string(status), EffectMayHaveOccurred: effect,
	}
	if execErr != nil {
		result.ErrorCode = botErrCodeOf(execErr)
		result.ErrorMessage = botErrMessage(execErr)
	}
	if outcome.ResultJSON != "" {
		result.Summary = botResultSummary(outcome.ResultJSON)
	} else {
		result.Summary = botResultSummary("{}")
	}
	raw, _ := json.Marshal(result)
	seq, seqErr := s.bots.NextMessageSeqTx(tx, call.BotID)
	if seqErr != nil {
		return seqErr
	}
	dedup := "terminal:" + call.ID
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: call.BotID, Seq: seq,
		RunID: runIDPtr(runID), CallID: &call.ID,
		Kind: model.BotMessageKindBackgroundResult, ContentJSON: string(raw),
		DedupKey: &dedup, CreatedAt: now,
	}
	if err := repository.NewBotMessageRepository(tx).AppendTx(tx, msg); err != nil {
		// 幂等：恢复/重放已追加过同样结果时跳过。
		if existing, findErr := repository.NewBotMessageRepository(tx).FindByDedupKey(call.BotID, dedup); findErr == nil {
			_ = existing
			return nil
		}
		return err
	}
	return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
		Kind: "tool_result", Phase: "terminal",
		Payload: map[string]interface{}{
			"status": string(status), "message_id": msg.ID,
			"tool_name": call.ToolName, "action": call.Action,
			"effect_may_have_occurred": effect,
		},
	})
}

// revertQueuedCall 授权失效时把排队调用退回待确认；事件带完整调用投影，
// 订阅端合并后立即恢复审批控件，不靠重读。
func (s *BotService) revertQueuedCall(call *model.BotToolCall) {
	ok, err := s.calls.UpdateWhere(call.ID, string(model.BotCallStatusQueued), map[string]interface{}{
		"status":           model.BotCallStatusPendingConfirmation,
		"authorization_id": nil,
	})
	if err != nil || !ok {
		return
	}
	call.Status = model.BotCallStatusPendingConfirmation
	call.AuthorizationID = nil
	s.emitEvent(call.BotID, nil, call.ID, model.BotEventToolCall, botEventEnvelope{
		Kind: "tool_call", Phase: "awaiting_confirmation",
		Payload: botToolCallResponseOf(call),
	})
}

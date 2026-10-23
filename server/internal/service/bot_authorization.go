package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 结构化「始终允许」：规则按 (tool, action, scope, 字段约束) 规范化哈希。
// 授权不能代替现有 service 的权限检查；worker 认领前重新核对。

// botAuthorizationRule 是规则的可哈希形态。
type botAuthorizationRule struct {
	ToolName      string   `json:"tool_name"`
	Action        string   `json:"action"`
	ScopeKind     string   `json:"scope_kind"`
	ScopeID       string   `json:"scope_id"`
	EnvironmentID string   `json:"environment_id,omitempty"`
	Fields        []string `json:"fields,omitempty"`
	// Param 是参数值级约束（如 source=github）：值不同副作用不同的动作按
	// 值绑定授权。
	Param string `json:"param,omitempty"`
}

// botRuleOf 由准备好的调用构造规则（字段约束排序后哈希，保证子集语义：
// 授权 {body} 只匹配恰好写 body 的调用；更宽的调用需要重新授权）。
func botRuleOf(prepared *botPreparedCall) botAuthorizationRule {
	fields := append([]string{}, prepared.FieldsConstraint...)
	sort.Strings(fields)
	envID := ""
	if prepared.EnvironmentID != nil {
		envID = *prepared.EnvironmentID
	}
	return botAuthorizationRule{
		ToolName:      prepared.ToolName,
		Action:        prepared.Action,
		ScopeKind:     prepared.ScopeKind,
		ScopeID:       prepared.ScopeID,
		EnvironmentID: envID,
		Fields:        fields,
		Param:         prepared.ParamConstraint,
	}
}

func botRuleHash(rule botAuthorizationRule) string {
	raw, _ := json.Marshal(rule)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// matchAuthorization 按规则哈希查活跃授权；无授权返回 nil。
func (s *BotService) matchAuthorization(botID string, prepared *botPreparedCall) *model.BotAuthorization {
	rule := botRuleOf(prepared)
	authz, err := s.authz.FindActiveRuleTx(s.db, botID, botRuleHash(rule))
	if err != nil {
		// 查询失败按无授权处理（调用转确认，方向安全），但要留痕——否则
		// 数据库降级时会无声地变成一场解释不清的确认卡风暴。
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("授权规则查询失败，按无授权处理",
				zap.String("bot_id", botID), zap.Error(err))
		}
		return nil
	}
	return authz
}

// ---------- 调用决策 ----------

// BotToolCallResponse 是调用的 API 投影；result_preview 进入响应，完整结果
// 只经 result 端点分页读取。
type BotToolCallResponse struct {
	ID                    string          `json:"id"`
	BotID                 string          `json:"bot_id"`
	RequestID             string          `json:"request_id"`
	ProviderToolCallID    string          `json:"provider_tool_call_id"`
	ToolName              string          `json:"tool_name"`
	Action                string          `json:"action"`
	Arguments             json.RawMessage `json:"arguments"`
	ExecutionMode         string          `json:"execution_mode"`
	RequiresConfirmation  bool            `json:"requires_confirmation"`
	Status                string          `json:"status"`
	Decision              *string         `json:"decision"`
	DecidedAt             *string         `json:"decided_at"`
	AuthorizationID       *string         `json:"authorization_id"`
	ExecutionContext      json.RawMessage `json:"execution_context"`
	Policy                json.RawMessage `json:"policy,omitempty"`
	CancelRequested       bool            `json:"cancel_requested"`
	ResultPreview         json.RawMessage `json:"result_preview"`
	ResultChars           int64           `json:"result_chars"`
	Truncated             bool            `json:"truncated"`
	ErrorCode             *string         `json:"error_code"`
	ErrorMessage          *string         `json:"error_message"`
	EffectMayHaveOccurred bool            `json:"effect_may_have_occurred"`
	CreatedAt             string          `json:"created_at"`
	StartedAt             *string         `json:"started_at"`
	FinishedAt            *string         `json:"finished_at"`
}

func botToolCallResponseOf(call *model.BotToolCall) BotToolCallResponse {
	resp := BotToolCallResponse{
		ID:                    call.ID,
		BotID:                 call.BotID,
		RequestID:             call.RequestID,
		ProviderToolCallID:    call.ProviderToolCallID,
		ToolName:              call.ToolName,
		Action:                call.Action,
		ExecutionMode:         call.ExecutionMode,
		RequiresConfirmation:  call.RequiresConfirmation,
		Status:                string(call.Status),
		Decision:              call.Decision,
		DecidedAt:             rfc3339(call.DecidedAt),
		AuthorizationID:       call.AuthorizationID,
		CancelRequested:       call.CancelRequestedAt != nil,
		ResultChars:           call.ResultChars,
		Truncated:             call.Truncated,
		ErrorCode:             call.ErrorCode,
		ErrorMessage:          call.ErrorMessage,
		EffectMayHaveOccurred: call.EffectMayHaveOccurred,
		CreatedAt:             call.CreatedAt.UTC().Format(time.RFC3339Nano),
		StartedAt:             rfc3339(call.StartedAt),
		FinishedAt:            rfc3339(call.FinishedAt),
	}
	if call.ArgumentsJSON != "" {
		resp.Arguments = json.RawMessage(call.ArgumentsJSON)
	}
	if call.ExecutionContextJSON != "" {
		resp.ExecutionContext = json.RawMessage(call.ExecutionContextJSON)
	}
	if call.PolicyJSON != "" {
		// 分级放行（INT-160）：评估结论进 API 投影，前端据此渲染「自动执行」
		// 徽标与降级/过期说明。
		resp.Policy = json.RawMessage(call.PolicyJSON)
	}
	if call.ResultPreviewJSON != nil {
		resp.ResultPreview = json.RawMessage(*call.ResultPreviewJSON)
	}
	return resp
}

// BotAuthorizationResponse 是授权的 API 投影。
type BotAuthorizationResponse struct {
	ID            string          `json:"id"`
	ToolName      string          `json:"tool_name"`
	Action        string          `json:"action"`
	ScopeKind     string          `json:"scope_kind"`
	ScopeID       string          `json:"scope_id"`
	EnvironmentID *string         `json:"environment_id"`
	Constraints   json.RawMessage `json:"constraints"`
	CreatedAt     string          `json:"created_at"`
	ExpiresAt     *string         `json:"expires_at"`
	RevokedAt     *string         `json:"revoked_at"`
}

func botAuthorizationResponseOf(a *model.BotAuthorization) BotAuthorizationResponse {
	return BotAuthorizationResponse{
		ID:            a.ID,
		ToolName:      a.ToolName,
		Action:        a.Action,
		ScopeKind:     a.ScopeKind,
		ScopeID:       a.ScopeID,
		EnvironmentID: a.EnvironmentID,
		Constraints:   json.RawMessage(a.ConstraintsJSON),
		CreatedAt:     a.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:     rfc3339(a.ExpiresAt),
		RevokedAt:     rfc3339(a.RevokedAt),
	}
}

// CallDecisionRequest 是确认卡片的决定。
type CallDecisionRequest struct {
	Decision string `json:"decision"`
}

// DecideCall 处理 approve/reject/always_allow：只对保存好的调用参数决策。
// 重复相同决定幂等返回；相互冲突的决定返回 409。
func (s *BotService) DecideCall(userID, botID, callID string, req *CallDecisionRequest) (*BotToolCallResponse, error) {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}
	call, err := s.calls.FindByID(callID)
	if err != nil || call.BotID != botID {
		return nil, errs.ErrBotCallNotFound
	}
	var decision model.BotCallDecision
	switch req.Decision {
	case string(model.BotDecisionApprove):
		decision = model.BotDecisionApprove
	case string(model.BotDecisionReject):
		decision = model.BotDecisionReject
	case string(model.BotDecisionAlwaysAllow):
		decision = model.BotDecisionAlwaysAllow
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code, "decision 必须是 approve/reject/always_allow")
	}

	// 已有决定：相同决定幂等返回（含已批准排队/执行中/已终结）；不同决定冲突。
	if call.Decision != nil && *call.Decision == string(decision) {
		if call.Status.IsTerminal() || call.Status == model.BotCallStatusQueued || call.Status == model.BotCallStatusRunning {
			resp := botToolCallResponseOf(call)
			return &resp, nil
		}
	}
	if call.Status != model.BotCallStatusPendingConfirmation {
		return nil, errs.ErrBotCallStateConflict
	}

	now := time.Now().UTC()
	decisionStr := string(decision)

	// always_allow 的精确授权值：同一事务中建立并批准当前调用。
	var newAuthz *model.BotAuthorization
	if decision == model.BotDecisionAlwaysAllow {
		// 定时任务的「始终允许」只留给 run（INT-136）：范围=单个任务 ID，
		// 用户此前已确认过该任务。create/update 的指令全文每次都可能不同，
		// 一旦豁免，模型改写任务指令时用户没有看过新指令，而任务以后每次
		// 运行都默认放行写操作，等于一次点击永久授权未知的指令；enable 是
		// 重新武装，delete 不可逆，同样不能一次点完永久生效。未列出的动作
		// ——包括未来新增动作——一律拒绝：受理端对未识别动作 fail-closed
		// 弹确认，这里若默认放行豁免，洞会原样重开。
		if call.ToolName == "fs_schedule" && call.Action != "run" {
			return nil, errs.New(errs.ErrInvalidParams.Code,
				"定时任务只有手动运行支持「始终允许」；创建/修改/启用/删除等其余动作每次都要确认")
		}
		newAuthz = s.buildAlwaysAllowValue(botID, call)
	}

	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txCalls := repository.NewBotToolCallRepository(tx)
		cols := map[string]interface{}{
			"status":     model.BotCallStatusQueued,
			"decision":   decisionStr,
			"decided_at": now,
		}
		if decision == model.BotDecisionReject {
			cols["status"] = model.BotCallStatusRejected
			cols["finished_at"] = now
		}
		if newAuthz != nil {
			authzRepo := repository.NewBotAuthorizationRepository(tx)
			if err := authzRepo.CreateTx(tx, newAuthz); err != nil {
				// 活跃规则唯一冲突：过期行也占着部分唯一索引（按
				// revoked_at IS NULL 判活跃）——惰性撤销过期行后重试一次；
				// 仍有活跃行则复用已有授权。
				if _, revErr := authzRepo.RevokeExpiredRuleTx(tx, botID, newAuthz.RuleHash, now); revErr != nil {
					return revErr
				}
				existing, findErr := authzRepo.FindActiveRuleTx(tx, botID, newAuthz.RuleHash)
				switch {
				case errors.Is(findErr, gorm.ErrRecordNotFound):
					// 撤销过期占位后已无活跃行：重试创建。
					if err2 := authzRepo.CreateTx(tx, newAuthz); err2 != nil {
						return err2
					}
				case findErr != nil:
					return findErr
				default:
					newAuthz = existing
				}
			}
			cols["authorization_id"] = newAuthz.ID
		}
		ok, err := txCalls.UpdateWhereTx(tx, callID, string(model.BotCallStatusPendingConfirmation), cols)
		if err != nil {
			return err
		}
		if !ok {
			return errs.ErrBotCallStateConflict
		}
		// 审计决定消息（幂等）。
		if err := s.appendAuditMessageTx(tx, botID, call, func(seq int64) *model.BotMessage {
			raw, _ := json.Marshal(botDecisionContent{CallID: callID, Decision: decisionStr})
			return &model.BotMessage{
				ID: uuid.NewString(), BotID: botID, Seq: seq,
				Kind: model.BotMessageKindApprovalDecision, ContentJSON: string(raw),
				DedupKey:  botStrPtr("decision:" + callID),
				CreatedAt: now,
			}
		}); err != nil {
			return err
		}
		// reject 的唯一终态结果随决定同事务落定（背景结果消息 + 事件）。
		if decision == model.BotDecisionReject {
			return s.appendBackgroundResultTx(tx, "", call, model.BotCallStatusRejected,
				botToolOutcome{}, errs.New(errs.ErrInvalidParams.Code, "用户拒绝了该操作"), false, now)
		}
		return nil
	})
	if errors.Is(txErr, errs.ErrBotCallStateConflict) {
		return nil, txErr
	}
	if txErr != nil {
		s.logger.Error("机器人调用决定事务失败", zap.String("call_id", callID), zap.Error(txErr))
		return nil, errs.New(errs.ErrInternal.Code, "决定事务失败: "+botErrMessage(txErr))
	}

	// 决定后事件与 worker 唤醒（事务外）。
	callIDCopy := callID
	// 分级放行误判检测（INT-160）：用户方向是事实源——拒批同目标写、或批准
	// 与刚自动放行动作方向相反的写，都按 (rule, scope) 记一次误判。
	s.detectPolicyMisjudgeOnDecision(botID, call, decisionStr)
	s.emitEvent(botID, nil, callIDCopy, model.BotEventToolResult, botEventEnvelope{
		Kind: "tool_result", Phase: "decision",
		Payload: map[string]interface{}{"decision": decisionStr, "status": map[bool]string{
			true: string(model.BotCallStatusRejected), false: string(model.BotCallStatusQueued),
		}[decision == model.BotDecisionReject]},
	})
	if decision != model.BotDecisionReject {
		s.enqueueCallWork(callID)
	} else {
		// reject 也是落库的模型输入：原 run 可能已结束，立即唤醒主线，
		// 不等扫描兜底（与取消同型）。
		s.wakeRuntime(botID, botWake{
			Trigger: model.BotTriggerToolResult,
			Key:     "tool_result:" + callID,
			Context: marshalJSONString(map[string]interface{}{"call_id": callID, "status": string(model.BotCallStatusRejected)}),
		})
	}

	fresh, err := s.calls.FindByID(callID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := botToolCallResponseOf(fresh)
	return &resp, nil
}

// buildAlwaysAllowValue 从调用账本重建规则（执行上下文里保存了受理时的
// scope 与字段约束），构造待创建的授权记录值。
func (s *BotService) buildAlwaysAllowValue(botID string, call *model.BotToolCall) *model.BotAuthorization {
	var execCtx botCallExecContext
	_ = json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx)
	envID := execCtx.EnvID
	var fields []string
	if len(execCtx.Fields) > 0 {
		// presentFields 返回声明序，botRuleOf 走排序口径——重建规则前
		// 必须同序，否则多字段调用的规则哈希永远对不上、always-allow
		// 静默失效成重复弹确认（代码审查 P2）。
		fields = append([]string{}, execCtx.Fields...)
		sort.Strings(fields)
	}
	constraints := map[string]interface{}{}
	if fields != nil {
		constraints["fields"] = fields
	}
	// 参数值级约束与规范化执行配置一并落库：只有 RuleHash 里带摘要时，
	// 授权管理面无法区分同 action 的不同取值规则、审计无法自证「按值
	// 精确授权」（代码审查 P2）。
	if execCtx.Param != "" {
		constraints["param"] = execCtx.Param
	}
	if execCtx.ExecConfig != "" {
		var cfg interface{}
		if err := json.Unmarshal([]byte(execCtx.ExecConfig), &cfg); err == nil {
			constraints["exec_config"] = cfg
		}
	}
	constraintsRaw, _ := json.Marshal(constraints)
	rule := botAuthorizationRule{
		ToolName:      call.ToolName,
		Action:        call.Action,
		ScopeKind:     execCtx.ScopeKind,
		ScopeID:       execCtx.ScopeID,
		EnvironmentID: envID,
		Fields:        fields,
		Param:         execCtx.Param,
	}
	var envPtr *string
	if envID != "" {
		envPtr = &envID
	}
	// INT-160：新建「始终允许」一律限时（默认 30 天）——永久、范围宽的授权
	// 是实测中最不安全的放宽形态；到期由匹配查询挡掉并惰性补 revoked_at。
	expiresAt := time.Now().UTC().Add(botPolicyAlwaysAllowTTL)
	return &model.BotAuthorization{
		ID:              uuid.NewString(),
		BotID:           botID,
		ToolName:        call.ToolName,
		Action:          call.Action,
		ScopeKind:       execCtx.ScopeKind,
		ScopeID:         execCtx.ScopeID,
		EnvironmentID:   envPtr,
		ConstraintsJSON: string(constraintsRaw),
		RuleHash:        botRuleHash(rule),
		CreatedAt:       time.Now().UTC(),
		ExpiresAt:       &expiresAt,
	}
}

// appendAuditMessageTx 在事务内取号追加消息（幂等：dedup 命中直接跳过）。
func (s *BotService) appendAuditMessageTx(tx *gorm.DB, botID string, call *model.BotToolCall, build func(seq int64) *model.BotMessage) error {
	txMsgs := repository.NewBotMessageRepository(tx)
	msg := build(0) // seq 占位
	if existing, err := txMsgs.FindByDedupKey(botID, *msg.DedupKey); err == nil {
		_ = existing
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return err
	}
	msg.Seq = seq
	msg.CallID = &call.ID
	return txMsgs.AppendTx(tx, msg)
}

func botStrPtr(s string) *string { return &s }

// ---------- 授权查询与撤销 ----------

func (s *BotService) ListAuthorizations(userID, botID string, activeOnly bool) ([]BotAuthorizationResponse, error) {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}
	rows, err := s.authz.ListByBot(botID, activeOnly)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotAuthorizationResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botAuthorizationResponseOf(&rows[i]))
	}
	return out, nil
}

// RevokeAuthorization 实际撤销（不物理删除）；对尚未认领的授权调用生效，
// 使其回到待确认；已经开始的操作不回滚。
func (s *BotService) RevokeAuthorization(userID, botID, authzID string) error {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrBotNotFound
		}
		return errs.ErrInternal
	}
	if _, err := s.authz.FindByIDAndBot(authzID, botID); err != nil {
		return errs.ErrBotAuthorizationNotFound
	}
	revoked, err := s.authz.Revoke(authzID, time.Now().UTC())
	if err != nil {
		return errs.ErrInternal
	}
	if !revoked {
		return nil // 幂等：已撤销
	}
	// 让引用该授权、仍在排队的调用回到待确认；事件带完整调用投影
	//（与 worker 认领前的撤销复核同一条退回路径）。
	var queued []*model.BotToolCall
	if err := s.db.Where("bot_id = ? AND authorization_id = ? AND status = ?",
		botID, authzID, model.BotCallStatusQueued).Find(&queued).Error; err == nil {
		for _, call := range queued {
			s.revertQueuedCall(call)
		}
	}
	// 分级放行误判检测（INT-160）：撤销同目标的授权说明此前按该规则的自动
	// 放行不合用户预期。授权行带工具/动作/范围，按事实匹配窗口内的自动放行。
	authzRow, err := s.authz.FindByIDAndBot(authzID, botID)
	if err == nil {
		if ruleID := botPolicyRuleForAction(authzRow.ToolName, authzRow.Action); ruleID != "" {
			envID := ""
			if authzRow.EnvironmentID != nil {
				envID = *authzRow.EnvironmentID
			}
			if s.policyRecentAutoOnThread(botID, ruleID, envID, authzRow.ScopeID) {
				s.recordPolicyMisjudgment(botID, ruleID, "用户撤销了同目标的授权")
			}
		}
	}
	return nil
}

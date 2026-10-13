package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 运行时请求应答与密钥代答（INT-85）。
//
// 应答前先取线程投影校验 requestId 属于本线程的 runtimeRequests（继承/fork
// 前缀里的请求不能在本线程原位应答），pending 之外返回 40916——这些判定与
// 上游 Orchestrator 的 respond 校验同构，提前发生以给出稳定的 Fast Ship
// 错误码。密钥路径有独立的安全约束：密钥值只在这一次请求里出现，不落库、
// 不写日志、不进错误信息或 RPC cause 回显。

// t3ValidApprovalDecisions 是上游 ProviderApprovalDecision 的取值集。
var t3ValidApprovalDecisions = map[string]bool{
	"accept": true, "acceptForSession": true, "acceptAlways": true,
	"decline": true, "cancel": true,
}

// RespondToRequest 应答审批或用户输入请求（runtime-request.respond）。
func (s *T3ThreadsService) RespondToRequest(ctx context.Context, userID, envID, threadID, requestID string, req *T3RespondRequest) (*T3DispatchResult, error) {
	if req == nil || threadID == "" || requestID == "" {
		return nil, errs.ErrInvalidParams
	}
	command := map[string]any{
		"type":      "runtime-request.respond",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
		"requestId": requestID,
	}
	switch req.Type {
	case "approval":
		if req.Decision == nil || !t3ValidApprovalDecisions[*req.Decision] {
			return nil, errs.New(errs.ErrInvalidParams.Code, "approval 应答需要有效的 decision")
		}
		command["decision"] = *req.Decision
	case "user_input":
		if req.Answers == nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, "user_input 应答需要 answers")
		}
		command["answers"] = map[string]any(req.Answers)
		if req.AttachmentsByQuestionId != nil {
			byQuestion := map[string]any{}
			for questionID, attachments := range req.AttachmentsByQuestionId {
				normalized, err := normalizeT3AttachmentInput(attachments)
				if err != nil {
					return nil, err
				}
				byQuestion[questionID] = normalized
			}
			command["attachmentsByQuestionId"] = byQuestion
		}
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code, "type 必须是 approval 或 user_input")
	}

	// 成员校验与状态判定：requestId 必须是本线程 runtimeRequests 的成员且
	// 仍为 pending（继承行的请求不能在本线程应答；已决请求返回 40916）。
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	projection, err := s.fetchProjectionForWrite(ctx, env, token, threadID)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	request, ok := findT3RuntimeRequest(projection, requestID)
	if !ok {
		return nil, errs.ErrT3ThreadItemNotFound
	}
	if request.Status != "pending" {
		return nil, errs.New(errs.ErrT3StateConflict.Code, "该请求已处理（"+request.Status+"）")
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	sequence, err := s.dispatchT3Command(ctx, session, threadID, command)
	if err != nil {
		// 上游竞态下的同构判定（请求在读取投影后被处理/移除）。
		if t3RequestConflictError(err) {
			return nil, errs.New(errs.ErrT3StateConflict.Code, "该请求已处理，请刷新后重试")
		}
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}

// findT3RuntimeRequest 在投影 runtimeRequests 中找请求记录。
func findT3RuntimeRequest(projection *t3ThreadProjection, requestID string) (t3RuntimeRequest, bool) {
	for _, request := range projection.RuntimeRequests {
		if request.ID == requestID {
			return request, true
		}
	}
	return t3RuntimeRequest{}, false
}

// DismissRequest 关闭用户输入请求卡片（thread.user-input.dismiss）。
func (s *T3ThreadsService) DismissRequest(ctx context.Context, userID, envID, threadID, requestID string) (*T3DispatchResult, error) {
	if threadID == "" || requestID == "" {
		return nil, errs.ErrInvalidParams
	}
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	projection, err := s.fetchProjectionForWrite(ctx, env, token, threadID)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	request, ok := findT3RuntimeRequest(projection, requestID)
	if !ok {
		return nil, errs.ErrT3ThreadItemNotFound
	}
	if request.Status != "pending" {
		return nil, errs.New(errs.ErrT3StateConflict.Code, "该请求已处理（"+request.Status+"）")
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	sequence, err := s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":      "thread.user-input.dismiss",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
		"requestId": requestID,
	})
	if err != nil {
		if t3RequestConflictError(err) {
			return nil, errs.New(errs.ErrT3StateConflict.Code, "该请求已处理，请刷新后重试")
		}
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}

// AnswerSecret 应答本线程 pending 的 secret_request（secrets.answerRequest）。
//
// 安全面：先验证合成 itemId 是本线程本地（visibility=local）pending 的
// secret_request，转出真实 turnItemId 再调用上游；上游失败只按 reason
// 枚举映射固定文案（40916/40419/50202），绝不透出上游 message/cause、
// 不记录请求载荷。密钥值不落库、不写日志。
func (s *T3ThreadsService) AnswerSecret(ctx context.Context, userID, envID, threadID, itemID string, req *T3AnswerSecretRequest) error {
	if req == nil || threadID == "" || itemID == "" {
		return errs.ErrInvalidParams
	}
	answerType := ""
	secret := ""
	switch req.Type {
	case "save":
		if req.Secret == nil || strings.TrimSpace(*req.Secret) == "" {
			return errs.New(errs.ErrInvalidParams.Code, "save 应答需要 secret")
		}
		answerType = "save"
		secret = *req.Secret
	case "decline":
		answerType = "decline"
	default:
		return errs.New(errs.ErrInvalidParams.Code, "type 必须是 save 或 decline")
	}

	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	projection, err := s.fetchProjectionForWrite(ctx, env, token, threadID)
	if err != nil {
		return s.upstreamError(err)
	}
	turnItemID, err := resolveT3SecretRequestItem(projection, threadID, itemID)
	if err != nil {
		return err
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return s.upstreamError(err)
	}
	defer session.Close()

	answer := map[string]any{"type": answerType}
	if answerType == "save" {
		answer["secret"] = secret
	}
	_, cause, callErr := session.callInspect(ctx, "secrets.answerRequest", map[string]any{
		"threadId":   threadID,
		"turnItemId": turnItemID,
		"answer":     answer,
	})
	if callErr != nil {
		if errors.Is(callErr, errT3CredentialRejected) {
			return errs.New(errs.ErrT3Upstream.Code, "T3 凭证失效，请重新配对")
		}
		if cause != nil {
			// 安全错误路径：只按 cause 里的 reason 枚举映射固定文案，
			// 不透出上游 message/cause。
			return t3SecretSafeErrorFromCause(cause)
		}
		// 传输层失败：固定文案，同样不携带详情。
		return errs.New(errs.ErrT3Upstream.Code, "密钥保存失败，请稍后重试")
	}
	return nil
}

// resolveT3SecretRequestItem 校验合成条目 ID 是本线程本地 pending 的
// secret_request，返回真实 turnItemId。
func resolveT3SecretRequestItem(projection *t3ThreadProjection, threadID, compositeItemID string) (string, error) {
	row, ok := findT3VisibleRow(projection.VisibleTurnItems, compositeItemID)
	if !ok {
		return "", errs.ErrT3ThreadItemNotFound
	}
	// 只认本线程本地条目：继承行（fork 前缀）的 secret 要回来源线程应答。
	if row.SourceThreadID != threadID {
		return "", errs.ErrT3ThreadItemNotFound
	}
	var item struct {
		ID           string `json:"id"`
		Type         string `json:"type"`
		SecretStatus string `json:"secretStatus"`
	}
	if err := json.Unmarshal(row.Item, &item); err != nil || item.ID == "" {
		return "", errs.ErrT3ThreadItemNotFound
	}
	if item.Type != "secret_request" {
		return "", errs.ErrT3ThreadItemNotFound
	}
	if item.SecretStatus != "pending" {
		return "", errs.New(errs.ErrT3StateConflict.Code, "该密钥请求已被回答")
	}
	return item.ID, nil
}

// t3SecretSafeErrorFromCause 把 secrets.answerRequest 的 Failure cause 翻译
// 成固定文案。只解析 SecretRequestError 的 reason 枚举（固定字符串集），
// 不读 message/cause 字段——密钥值与任何上游细节都不进错误信息。
func t3SecretSafeErrorFromCause(cause []rpcFailureCause) error {
	if len(cause) == 0 || cause[0].Tag != "Fail" {
		return errs.New(errs.ErrT3Upstream.Code, "密钥保存失败，请稍后重试")
	}
	var tagged struct {
		Tag    string `json:"_tag"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(cause[0].Error, &tagged); err != nil || tagged.Tag != "SecretRequestError" {
		return errs.New(errs.ErrT3Upstream.Code, "密钥保存失败，请稍后重试")
	}
	switch tagged.Reason {
	case "already_answered":
		return errs.New(errs.ErrT3StateConflict.Code, "该密钥请求已被回答")
	case "agent_stopped":
		return errs.New(errs.ErrT3StateConflict.Code, "发起请求的代理已停止，无法使用该密钥")
	case "not_found":
		return errs.ErrT3ThreadItemNotFound
	case "load_failed":
		return errs.New(errs.ErrT3Upstream.Code, "密钥请求读取失败，请刷新后重试")
	default:
		// store_failed / record_failed / ref_* 等：一律固定文案，不携带详情。
		return errs.New(errs.ErrT3Upstream.Code, "密钥保存失败，请稍后重试")
	}
}

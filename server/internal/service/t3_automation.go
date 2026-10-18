package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

// T3 自动化管理命令（INT-90）：settle/archive 不公开 REST，仅供自动化后置
// 操作经协调循环下发。协议事实（核对自 T3 源码 orchestrationV2.ts）：
//
//	thread.settle:   {type, commandId, threadId, settledAt?}
//	thread.archive:  {type, commandId, threadId}
//
// 两者都经 orchestration.dispatchCommand 下发，需要 orchestration:operate。

// t3PostOutcome 后置命令的下达结果分类，决定协调循环的下一步：
//   - Accepted：上游确认受理（或 commandId 冲突=此前已受理过），后置成功
//   - Rejected：上游明确拒绝（业务/校验失败），换新 commandId 才有意义
//   - TransportError：连接层失败，命令可能已送达——原 commandId 重试
type t3PostOutcome int

const (
	t3PostAccepted t3PostOutcome = iota
	t3PostRejected
	t3PostTransportError
)

const t3PostConflictMarker = "OrchestratorCommandIdConflictError"

// ManageThread 下发 thread.settle / thread.archive（postAction 决定命令类型）。
// commandId 由调用方按 attempt 派生；网络失败重放同 ID，commandId 冲突视为
// 已受理成功。
func (s *T3ThreadsService) ManageThread(
	ctx context.Context, userID, envID, threadID string,
	action model.AutomationPostAction, commandID string,
) (t3PostOutcome, error) {
	var commandType string
	switch action {
	case model.AutomationPostSettle:
		commandType = "thread.settle"
	case model.AutomationPostArchive:
		commandType = "thread.archive"
	default:
		return t3PostRejected, errs.New(errs.ErrInvalidParams.Code, "未知的后置操作 "+string(action))
	}
	if strings.TrimSpace(threadID) == "" {
		return t3PostRejected, errs.ErrInvalidParams
	}

	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return t3PostRejected, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		return t3PostTransportError, err
	}
	defer session.Close()

	command := map[string]any{
		"type":      commandType,
		"commandId": commandID,
		"threadId":  threadID,
	}
	// settledAt 不传：T3 以受理时间入账，Fast Ship 不指定历史时间点。

	id := session.nextID
	session.nextID++
	if err := session.sendRequest(ctx, id, "orchestration.dispatchCommand", command); err != nil {
		return t3PostTransportError, err
	}
	for {
		frame, err := session.readFrame(ctx)
		if err != nil {
			return t3PostTransportError, err
		}
		if !frame.isExitFor(id) {
			continue
		}
		value, exitErr := frame.exitValue()
		if exitErr == nil {
			_ = value
			return t3PostAccepted, nil
		}
		if strings.Contains(exitErr.Error(), t3PostConflictMarker) {
			// 同 commandId 已受理过（重放）：按成功处理。
			return t3PostAccepted, nil
		}
		return t3PostRejected, exitErr
	}
}

// ThreadSettleSafe 报告线程当前是否可以安全收尾（settle 前置）：无活动运行、
// 无待用户输入/审批、无占完成位的后台工作。运行态只取 hub 可信活跃面
// （Ready）——陈旧/同步中的快照一律 50202，由调用方按重试处理。找不到线程
// 时同样报错（线程可能已被上游删除）。
func (s *T3ThreadsService) ThreadSettleSafe(ctx context.Context, userID, envID, threadID string) (bool, error) {
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errs.ErrT3EnvironmentNotFound
		}
		return false, errs.ErrInternal
	}

	token, err := s.bearerToken(env)
	if err != nil {
		return false, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	// 等待预算用 ShellFirstSync：这里可能是在等 hub 首次同步，不是每请求
	// 预算的放大；entry 建立后近似零成本。
	ctx, cancel := context.WithTimeout(ctx, s.budgets.ShellFirstSync)
	defer cancel()

	shell, _, err := s.trustedShell(ctx, env, token, threadID)
	if err != nil {
		return false, err
	}
	switch t3ThreadStatus(shell) {
	case api.T3ThreadSummaryStatusIdle, api.T3ThreadSummaryStatusArchived:
		return true, nil
	default:
		return false, nil
	}
}

// CreateThreadForAutomation 同 CreateThread，但额外报告「launch RPC 是否已发出」：
// 发出后失联（结果未确认）与未发出（明确失败）在自动化派发里是两种处置——
// 前者保留预分配 thread_id 等回报或 deadline，后者直接失败。JWT REST 路径
// 继续走 CreateThread，语义不变（INT-85）。
func (s *T3ThreadsService) CreateThreadForAutomation(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (result *T3ThreadLaunchResult, launchSent bool, err error) {
	return s.createThread(ctx, userID, envID, req)
}

// createThread 是 CreateThread 的实现体；launchSent 报告 launchThread 请求帧
// 是否已写出（写出后无论结果如何都视为「已进入 RPC」）。项目表走 HTTP
// `/api/projects`（INT-116）：不再为启动线程拉全量 shell 快照、不建 shell
// 订阅；fetchProjectSnapshot 已分类凭证被拒，无需前置会话探测。
func (s *T3ThreadsService) createThread(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (*T3ThreadLaunchResult, bool, error) {
	if req == nil || req.ProjectId == "" {
		return nil, false, errs.ErrInvalidParams
	}
	if !t3MessageHasContent(req.Message.Text, req.Message.Attachments) {
		return nil, false, errs.New(errs.ErrInvalidParams.Code, "消息正文与附件不能同时为空")
	}
	attachments, err := normalizeT3AttachmentInput(req.Message.Attachments)
	if err != nil {
		return nil, false, err
	}

	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, false, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, false, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	// 项目表（workspaceRoot/defaultModelSelection）全从这里来。
	projectShells, err := s.http.fetchProjectSnapshot(ctx, env.Address, token)
	if err != nil {
		return nil, false, s.upstreamError(err)
	}
	var workspaceRoot string
	var projectDefault *t3ModelSelection
	for _, project := range projectShells {
		if project.ID == req.ProjectId {
			if project.WorkspaceRoot != nil {
				workspaceRoot = *project.WorkspaceRoot
			}
			projectDefault = project.DefaultModelSelection
		}
	}
	if workspaceRoot == "" {
		return nil, false, errs.New(40401, "T3 项目不在该环境（或项目缺少 workspaceRoot）")
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, false, s.upstreamError(err)
	}
	defer session.Close()

	// 模型选择：请求缺省时回落项目默认（shell 快照 defaultModelSelection）。
	modelSelection := t3ModelSelectionWire(req.ModelSelection)
	if modelSelection == nil {
		modelSelection = t3ModelSelectionWire(newT3ModelSelectionAPI(projectDefault))
		if modelSelection == nil {
			return nil, false, errs.New(errs.ErrInvalidParams.Code, "缺少模型选择（请求未携带且项目无默认值）")
		}
	}

	workspace, err := s.t3ThreadWorkspaceWire(ctx, session, workspaceRoot, req.Workspace)
	if err != nil {
		return nil, false, err
	}

	runtimeMode := "full-access"
	if req.RuntimeMode != nil && *req.RuntimeMode != "" {
		switch *req.RuntimeMode {
		case "approval-required", "auto-accept-edits", "auto", "full-access":
			runtimeMode = string(*req.RuntimeMode)
		default:
			return nil, false, errs.New(errs.ErrInvalidParams.Code, "未知的 runtime_mode "+string(*req.RuntimeMode))
		}
	}

	title := ""
	generateTitle := true
	if req.Title != nil && strings.TrimSpace(*req.Title) != "" {
		title = strings.TrimSpace(*req.Title)
		generateTitle = false
	} else {
		title = t3TitleSeed(req.Message.Text, req.Message.Attachments)
	}

	payload := map[string]any{
		"commandId":         firstNonEmptyID(req.CommandId),
		"creationSource":    "web",
		"threadId":          firstNonEmptyID(req.ThreadId),
		"projectId":         req.ProjectId,
		"title":             title,
		"generateTitle":     generateTitle,
		"modelSelection":    modelSelection,
		"runtimeMode":       runtimeMode,
		"interactionMode":   "default",
		"workspaceStrategy": workspace,
		"initialMessage": map[string]any{
			"messageId":   firstNonEmptyID(req.Message.MessageId),
			"text":        req.Message.Text,
			"attachments": attachments,
		},
	}
	value, launchSent, err := session.callTracked(ctx, "orchestration.launchThread", payload)
	if err != nil {
		return nil, launchSent, s.upstreamError(err)
	}
	var result struct {
		ThreadId string `json:"threadId"`
		Resumed  *bool  `json:"resumed"`
	}
	if err := json.Unmarshal(value, &result); err != nil || result.ThreadId == "" {
		return nil, launchSent, errs.New(errs.ErrT3Upstream.Code, "启动结果解析失败")
	}
	return &T3ThreadLaunchResult{
		ThreadId: result.ThreadId,
		Resumed:  api.Deref(result.Resumed),
	}, launchSent, nil
}

// callTracked 同 call，但报告请求帧是否已写出。写出成功即视为已进入 RPC——
// 服务端可能已受理而响应未达；写出失败则上游必然没有收到。
func (s *t3RPCSession) callTracked(ctx context.Context, method string, payload any) (json.RawMessage, bool, error) {
	id := s.nextID
	s.nextID++
	defer s.closeOnCancel(ctx)()
	if err := s.sendRequest(ctx, id, method, payload); err != nil {
		return nil, false, err
	}
	for {
		frame, err := s.readFrame(ctx)
		if err != nil {
			return nil, true, err
		}
		if !frame.isExitFor(id) {
			if frame.isChunkFor(id) {
				if err := s.sendAck(ctx, frame.RequestID); err != nil {
					return nil, true, fmt.Errorf("发送 Ack 失败: %w", err)
				}
			}
			continue
		}
		value, err := frame.exitValue()
		if err != nil {
			return nil, true, err
		}
		return value, true, nil
	}
}

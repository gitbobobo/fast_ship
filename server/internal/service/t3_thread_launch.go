package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// CreateThreadForAgent 同 CreateThread，但额外报告「launch RPC 是否已发出」：
// 发出后失联（结果未确认）与未发出（明确失败）对系统派发方（机器人工具、
// 审查运行派发）是两种处置——前者保留预分配 thread_id 等回报或 deadline，
// 后者直接失败。JWT REST 路径继续走 CreateThread，语义不变（INT-85）。
func (s *T3ThreadsService) CreateThreadForAgent(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (result *T3ThreadLaunchResult, launchSent bool, err error) {
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

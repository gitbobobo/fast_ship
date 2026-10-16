package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// fs_thread / fs_environment 适配：只操作已连接环境；T3 的 scope/operate 守卫
// 继续由现有 service 执行，不扩大远端权限。

func (s *BotService) execThreadRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botThreadArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	switch call.Action {
	case "list":
		list, err := s.threads.List(ctx, userID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimThreadList(list))
	case "get", "item":
		if call.Action == "get" {
			detail, err := s.threads.Detail(ctx, userID, args.EnvID, args.ThreadID)
			if err != nil {
				return "", err
			}
			return botMarshalResult(map[string]interface{}{"thread": detail.Thread, "queue": detail.Queue, "activity": detail.Activity})
		}
		item, err := s.threads.ItemDetail(ctx, userID, args.EnvID, args.ThreadID, args.ItemID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimThreadItem(item))
	case "composer_options":
		opts, err := s.threads.ComposerOptions(ctx, userID, args.EnvID)
		if err != nil {
			return "", err
		}
		// 只透出启动器需要的项目与模型选择，不透传 T3 全量配置。
		return botMarshalResult(map[string]interface{}{"projects": opts.Projects})
	case "workspace_options":
		opts, err := s.threads.WorkspaceOptions(ctx, userID, args.EnvID, args.ProjectID, args.Query, args.Cursor)
		if err != nil {
			return "", err
		}
		return botMarshalResult(opts)
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_thread 不支持该读取动作")
}

func (s *BotService) execEnvironmentRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	userID := s.botUserIDOf(call.BotID)
	switch call.Action {
	case "list":
		envs, err := s.threads.List(ctx, userID)
		if err != nil {
			return "", err
		}
		out := make([]map[string]interface{}, 0, len(envs.Environments))
		for _, env := range envs.Environments {
			out = append(out, map[string]interface{}{
				"id":     env.Id,
				"name":   env.Name,
				"status": string(env.Status),
				"access": string(env.Access),
			})
		}
		return botMarshalResult(map[string]interface{}{"environments": out})
	case "projects":
		projects, err := s.bindings.DiscoverProjects(ctx, userID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(map[string]interface{}{"projects": projects})
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_environment 不支持该动作")
}

// runThreadWrite 在 worker 内执行 fs_thread 写操作。T3 的 command/thread/
// message ID 在受理时由服务端生成（execution_context），不让模型指定幂等身份。
func (s *BotService) runThreadWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var execCtx botCallExecContext
	_ = json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx)

	var args botThreadArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	switch call.Action {
	case "launch":
		req := &T3CreateThreadRequest{
			ProjectId: args.ProjectID,
			Message:   botThreadMessage(args.Text),
		}
		if title := strings.TrimSpace(args.Title); title != "" {
			req.Title = &title
		}
		if execCtx.ThreadID != "" {
			req.ThreadId = &execCtx.ThreadID
		}
		// 复用 launchSent 语义：结果只表示启动 RPC 的结果，不代表远端完成。
		result, _, err := s.threads.CreateThreadForAutomation(ctx, userID, args.EnvID, req)
		return result, err
	case "send":
		req := &T3SendMessageRequest{Text: args.Text}
		if execCtx.CommandID != "" {
			req.CommandId = &execCtx.CommandID
		}
		if execCtx.MessageID != "" {
			req.MessageId = &execCtx.MessageID
		}
		return s.threads.SendMessage(ctx, userID, args.EnvID, args.ThreadID, req)
	case "stop":
		return s.threads.StopThread(ctx, userID, args.EnvID, args.ThreadID)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "fs_thread 不支持该写动作")
}

// botCallExecContext 是调用记录 execution_context 的统一形态：服务端生成的
// T3 幂等身份 + 受理时解析的授权范围。
type botCallExecContext struct {
	ScopeKind string   `json:"scope_kind,omitempty"`
	ScopeID   string   `json:"scope_id,omitempty"`
	EnvID     string   `json:"env_id,omitempty"`
	ThreadID  string   `json:"thread_id,omitempty"`
	CommandID string   `json:"command_id,omitempty"`
	MessageID string   `json:"message_id,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	// Param 与授权规则的参数值级约束一致（如 source=github）：always-allow
	// 重建规则时必须有它，否则后续调用哈希永远对不上。
	Param string `json:"param,omitempty"`
}

func botThreadMessage(text string) api.CreateT3ThreadRequest_Message {
	return api.CreateT3ThreadRequest_Message{Text: text}
}

// resolveThreadScope 建卡前解析 fs_thread 写操作范围。
func (s *BotService) resolveThreadScope(prepared *botPreparedCall, raw map[string]json.RawMessage) {
	envID := rawString(raw["env_id"])
	switch prepared.Action {
	case "launch":
		prepared.ScopeKind = botScopeThreadLaunch
		prepared.ScopeID = rawString(raw["project_id"])
		prepared.EnvironmentID = &envID
	case "send":
		prepared.ScopeKind = botScopeThread
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	case "stop":
		prepared.ScopeKind = botScopeThreadStop
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	}
}

// buildThreadExecIDs 在受理时为写操作生成 T3 幂等身份（不让模型指定）。
func buildThreadExecIDs(action string) (threadID, commandID, messageID string) {
	switch action {
	case "launch":
		return uuid.NewString(), "", ""
	case "send":
		return "", uuid.NewString(), uuid.NewString()
	}
	return "", "", ""
}

func botSlimThreadList(list *T3ThreadListResponse) map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list.Items))
	for _, t := range list.Items {
		out = append(out, map[string]interface{}{
			"id":             t.Id,
			"title":          t.Title,
			"status":         string(t.Status),
			"environment_id": t.EnvironmentId,
			"project_id":     t.ProjectId,
			"project_name":   t.ProjectName,
			"settled":        t.Settled,
		})
	}
	return map[string]interface{}{"threads": out}
}

func botSlimThreadItem(item *T3ThreadItem) map[string]interface{} {
	out := map[string]interface{}{
		"id":         item.Id,
		"type":       string(item.Type),
		"status":     item.Status,
		"updated_at": item.UpdatedAt,
	}
	if item.Summary != "" {
		out["summary"] = item.Summary
	}
	if item.Text != "" {
		out["text"] = item.Text
	}
	return out
}

package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// fs_issue 适配：读取直连 IssueService；写操作由后台 worker 执行。
// userID 始终取机器人归属人；actor 由服务端生成，不接受模型传入。
// 动作注册表 spec 组见文件末尾（INT-195）。

const botIssueActor = "bot"

// fs_issue 各读取动作的入口（注册表 SyncExec 指向这些方法表达式）：解析参数
// 后各自投影；分页校验与瘦身投影保持共享。

func (s *BotService) execIssueList(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	page, pageSize, err := botValidatePage(args.Page, args.PageSize)
	if err != nil {
		return "", err
	}
	filters := IssueListFilters{
		State:    args.State,
		Query:    args.Query,
		Label:    args.Label,
		Source:   args.Source,
		Workflow: args.Workflow,
		Sort:     args.Sort,
	}
	items, total, err := s.issues.List(args.ProjectID, s.botUserIDOf(call.BotID), filters, page, pageSize)
	if err != nil {
		return "", err
	}
	return botMarshalResult(botSlimPage("issues", botSlimIssues(items), total, page, pageSize))
}

func (s *BotService) execIssueGet(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	issue, err := s.issues.Get(args.IssueID, s.botUserIDOf(call.BotID))
	if err != nil {
		return "", err
	}
	return botMarshalResult(botSlimIssueDetail(issue))
}

func (s *BotService) execIssueComments(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	page, pageSize, err := botValidatePage(args.Page, args.PageSize)
	if err != nil {
		return "", err
	}
	items, total, err := s.issues.ListComments(args.IssueID, s.botUserIDOf(call.BotID), page, pageSize)
	if err != nil {
		return "", err
	}
	return botMarshalResult(botSlimPage("comments", botSlimIssueComments(items), total, page, pageSize))
}

func (s *BotService) execIssueTimeline(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	page, pageSize, err := botValidatePage(args.Page, args.PageSize)
	if err != nil {
		return "", err
	}
	items, total, err := s.issues.ListTimeline(args.IssueID, s.botUserIDOf(call.BotID), page, pageSize)
	if err != nil {
		return "", err
	}
	return botMarshalResult(botSlimPage("timeline", botSlimIssueTimeline(items), total, page, pageSize))
}

// botSlimIssues 列表投影：保工具决策所需的最小字段。
func botSlimIssues(items []IssueResponse) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		out = append(out, botSlimIssueListItem(&items[i]))
	}
	return out
}

// botSlimIssueListItem 是列表项的精简形状：列表保持轻量，详情走 get。
// 外部文本由模型输入的出口统一围栏（botFenceToolResult），这里只负责瘦身。
func botSlimIssueListItem(i *IssueResponse) map[string]interface{} {
	out := map[string]interface{}{
		"id":         i.Id,
		"seq":        i.SequenceNumber,
		"title":      i.Title,
		"state":      string(i.State),
		"source":     string(i.Source),
		"project_id": i.ProjectId,
	}
	if i.InternalMeta != nil {
		out["workflow_status"] = string(i.InternalMeta.WorkflowStatus)
	}
	return out
}

// botSlimIssueDetail 是 get 的详情投影：正文、清单、标签等做决策所需的完整
// 信息（列表不携带）。labels/checklist 恒在——没有就是空数组，让调用方能
// 区分「没有清单/标签」和「字段没实现」。updated_at / checklist_updated_at
// 是后续写操作的写入保护基准，必须原样给出。
func botSlimIssueDetail(i *IssueResponse) map[string]interface{} {
	out := botSlimIssueListItem(i)
	out["reference"] = i.Reference
	out["body"] = i.Body
	out["state_reason"] = i.StateReason
	out["author"] = i.Author.Login
	out["updated_at"] = i.UpdatedAt
	out["created_at"] = i.CreatedAt
	out["labels"] = []api.IssueLabel{}
	out["checklist"] = []api.IssueChecklistItem{}
	if i.InternalMeta != nil {
		if len(i.InternalMeta.Labels) > 0 {
			out["labels"] = i.InternalMeta.Labels
		}
		if len(i.InternalMeta.Checklist) > 0 {
			out["checklist"] = i.InternalMeta.Checklist
		}
		if i.InternalMeta.ProgressPercent != nil {
			out["progress_percent"] = *i.InternalMeta.ProgressPercent
		}
		if i.InternalMeta.ChecklistUpdatedAt != nil {
			out["checklist_updated_at"] = *i.InternalMeta.ChecklistUpdatedAt
		}
	}
	if i.PullRequestSummary != nil {
		out["pull_request_summary"] = i.PullRequestSummary
	}
	return out
}

// botSlimIssueComments 是 comments 的精简投影：只给模型决策所需的字段，
// 不再整包透出 api.IssueComment；body 是外部内容，进不可信围栏（INT-136）。
func botSlimIssueComments(items []IssueCommentResponse) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		c := &items[i]
		out = append(out, map[string]interface{}{
			"id":         c.Id,
			"source":     string(c.Source),
			"author":     c.Author.Login,
			"body":       c.Body,
			"created_at": c.CreatedAt,
		})
	}
	return out
}

// botSlimIssueTimeline 是 timeline 的精简投影：不再整包透出
// api.IssueTimelineEvent；summary/body 是外部内容，进不可信围栏（INT-136）。
// 空串字段省略，投影保持轻量。
func botSlimIssueTimeline(items []IssueTimelineEventResponse) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		e := &items[i]
		item := map[string]interface{}{
			"id":         e.Id,
			"event_type": e.EventType,
			"created_at": e.CreatedAt,
		}
		if e.Actor.Login != "" {
			item["actor"] = e.Actor.Login
		}
		if e.Summary != "" {
			item["summary"] = e.Summary
		}
		if e.Body != "" {
			item["body"] = e.Body
		}
		out = append(out, item)
	}
	return out
}

// botIssueConflictResult 把「他人已改动，本次未执行」回给模型：带上目标最新
// 内容，便于重新读取并保留双方修改后再提交，而不是静默覆盖。current 与
// get 同一投影，外部字段同样进围栏（INT-136）。
func botIssueConflictResult(reason string, current *IssueResponse) map[string]interface{} {
	return map[string]interface{}{
		"conflict": true,
		"reason":   reason,
		"note":     "目标已被他人改动，本次未执行；请基于下面的最新内容重新读取，保留双方修改后再提交",
		"current":  botSlimIssueDetail(current),
	}
}

// fs_issue 各写动作的 worker 执行入口（注册表 AsyncExec 直接指向单动作方法；
// botIssueActor 语义同前）。

func (s *BotService) runIssueCreate(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	req := CreateInternalIssueRequest{
		Title: strings.TrimSpace(args.Title),
		Body:  args.Body,
	}
	source := strings.TrimSpace(args.Source)
	if source == "" || source == "internal" {
		created, err := s.issues.CreateInternalIssue(args.ProjectID, userID, req)
		if err != nil {
			return nil, err
		}
		return botSlimIssueDetail(created), nil
	}
	if source == "github" {
		created, err := s.issues.CreateGitHubIssueContext(ctx, args.ProjectID, userID, req)
		if err != nil {
			return nil, err
		}
		return botSlimIssueDetail(created), nil
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "source 只支持 internal/github")
}

func (s *BotService) runIssueUpdate(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	// 写入保护：确认期间目标被别人改过就不执行，回冲突与最新内容。
	if expected := strings.TrimSpace(args.ExpectedUpdatedAt); expected != "" {
		current, err := s.issues.Get(args.IssueID, userID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(current.UpdatedAt) != expected {
			return botIssueConflictResult("issue_updated", current), nil
		}
	}
	req := UpdateInternalIssueRequest{}
	// 只在调用参数真的带了 body 时才替换；字段级更新不得清空正文。
	if args.Body != nil {
		req.Body = args.Body
	}
	if strings.TrimSpace(args.Title) != "" {
		title := strings.TrimSpace(args.Title)
		req.Title = &title
	}
	if args.Labels != nil {
		req.Labels = *args.Labels
	}
	if args.StateReason != nil {
		reason := api.UpdateIssueRequestStateReason(*args.StateReason)
		req.StateReason = &reason
	}
	if args.State != "" {
		state := model.IssueState(args.State)
		req.State = &state
	}
	updated, err := s.issues.UpdateInternalIssueContext(ctx, args.IssueID, userID, req)
	if err != nil {
		return nil, err
	}
	return botSlimIssueDetail(updated), nil
}

func (s *BotService) runIssueSetWorkflow(_ context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	status := model.IssueWorkflowStatus(strings.TrimSpace(args.WorkflowStatus))
	meta, err := s.issues.UpdateInternalMeta(args.IssueID, userID, status, botIssueActor)
	if err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *BotService) runIssueReplaceChecklist(_ context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	// 写入保护：清单整组替换前核对上次读到的清单版本。
	if expected := strings.TrimSpace(args.ExpectedChecklistUpdatedAt); expected != "" {
		current, err := s.issues.Get(args.IssueID, userID)
		if err != nil {
			return nil, err
		}
		currentVersion := ""
		if current.InternalMeta != nil && current.InternalMeta.ChecklistUpdatedAt != nil {
			currentVersion = strings.TrimSpace(*current.InternalMeta.ChecklistUpdatedAt)
		}
		if currentVersion != expected {
			return botIssueConflictResult("checklist_updated", current), nil
		}
	}
	req := ReplaceIssueChecklistRequest{Items: args.Items}
	meta, err := s.issues.ReplaceChecklist(args.IssueID, userID, req, botIssueActor)
	if err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *BotService) runIssueComment(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	req := CreateInternalIssueCommentRequest{Body: args.CommentBody}
	// 复用已有幂等入口；幂等键是服务端调用 ID，不让模型指定。
	comment, err := s.issues.CreateInternalCommentIdempotentContext(ctx, args.IssueID, userID, req, botIssueActor, call.ID)
	if err != nil {
		return nil, err
	}
	return comment, nil
}

// resolveIssueScope 建卡前解析 fs_issue 写操作范围：指定 Fast Ship 项目。
func (s *BotService) resolveIssueScope(bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage) {
	prepared.ScopeKind = botScopeIssueProject
	switch prepared.Action {
	case "create":
		prepared.ScopeID = rawString(raw["project_id"])
		// source 决定副作用落点（internal 建平台 issue / github 建 GitHub
		// issue），授权规则必须按来源绑定：internal 的「总是允许」不得
		// 覆盖 github 来源的建卡。
		source := rawString(raw["source"])
		if source == "" {
			source = "internal"
		}
		prepared.ParamConstraint = "source=" + source
	default:
		projectID, source, err := s.issueScopeOf(rawString(raw["issue_id"]), bot.UserID)
		if err != nil {
			// 找不到目标：保持空 scope，执行阶段会以同样错误失败；
			// 这里不建卡也不伪造范围。
			prepared.ScopeID = ""
			return
		}
		prepared.ScopeID = projectID
		// 目标 issue 的来源决定副作用落点（internal 只写平台 / github 走
		// GitHub 写入），update/comment/set_workflow/replace_checklist 的
		// 「总是允许」都必须按来源绑定。
		prepared.ParamConstraint = "source=" + source
	}
	if prepared.Action == "update" {
		prepared.FieldsConstraint = presentFields(raw, "title", "body", "state", "state_reason", "labels")
	}
}

// issueScopeOf 读取 issue 归属项目与来源（同时校验归属人）：两者都是
// 授权规则的组成部分。
func (s *BotService) issueScopeOf(issueID, userID string) (string, string, error) {
	issue, err := s.issues.Get(issueID, userID)
	if err != nil {
		return "", "", err
	}
	return issue.ProjectId, string(issue.Source), nil
}

// botIssueActionSpecs 是 fs_issue 的动作声明（INT-195）：全部写动作 tier3、
// 范围解析（issue→project 回填）随 spec 声明，scope 细节在 resolveIssueScope。
// 读动作也挂同一 Scope：与拆分前一致（resolveCallScope 对每个受理调用都跑
// 范围解析，旧 resolveIssueScope 在分支前置 ScopeKind=issue_project，读动作
// 的 execution_context 同样带归属项目与 source 约束），不是只给写动作。
func botIssueActionSpecs() []*botActionSpec {
	scope := func(s *BotService, bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage, _ *botCallExecContext) error {
		s.resolveIssueScope(bot, prepared, raw)
		return nil
	}
	return []*botActionSpec{
		{
			Tool: "fs_issue", Action: "list",
			Description: "按项目列出 Issue",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("project_id"),
			SyncExec: (*BotService).execIssueList,
			Scope:    scope,
		},
		{
			Tool: "fs_issue", Action: "get",
			Description: "读取 Issue 详情",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("issue_id"),
			SyncExec: (*BotService).execIssueGet,
			Scope:    scope,
		},
		{
			Tool: "fs_issue", Action: "comments",
			Description: "读取 Issue 评论",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("issue_id"),
			SyncExec: (*BotService).execIssueComments,
			Scope:    scope,
		},
		{
			Tool: "fs_issue", Action: "timeline",
			Description: "读取 Issue 时间线",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("issue_id"),
			SyncExec: (*BotService).execIssueTimeline,
			Scope:    scope,
		},
		{
			Tool: "fs_issue", Action: "create",
			Description: "创建 Issue（内部或 GitHub）",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways,
			Required:   req("project_id", "title"),
			PolicyTier: "tier3",
			AsyncExec:  (*BotService).runIssueCreate,
			Scope:      scope,
		},
		{
			Tool: "fs_issue", Action: "update",
			Description: "更新 Issue 标题/正文/状态/标签",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways,
			Required:   req("issue_id"),
			PolicyTier: "tier3",
			AsyncExec:  (*BotService).runIssueUpdate,
			Scope:      scope,
		},
		{
			Tool: "fs_issue", Action: "set_workflow",
			Description: "设置工作流状态",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways,
			Required:   req("issue_id", "workflow_status"),
			PolicyTier: "tier3",
			AsyncExec:  (*BotService).runIssueSetWorkflow,
			Scope:      scope,
		},
		{
			Tool: "fs_issue", Action: "replace_checklist",
			Description: "整组替换验收清单",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways,
			Required:   req("issue_id", "items"),
			PolicyTier: "tier3",
			AsyncExec:  (*BotService).runIssueReplaceChecklist,
			Scope:      scope,
		},
		{
			Tool: "fs_issue", Action: "comment",
			Description: "发表 Issue 评论",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways,
			Required:   req("issue_id", "comment_body"),
			PolicyTier: "tier3",
			AsyncExec:  (*BotService).runIssueComment,
			Scope:      scope,
		},
	}
}

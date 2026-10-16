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

const botIssueActor = "bot"

func (s *BotService) execIssueRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	switch call.Action {
	case "list":
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
		items, total, err := s.issues.List(args.ProjectID, userID, filters, page, pageSize)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimPage("issues", botSlimIssues(items), total, page, pageSize))
	case "get", "comments", "timeline":
		switch call.Action {
		case "get":
			issue, err := s.issues.Get(args.IssueID, userID)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimIssue(issue))
		case "comments":
			page, pageSize, pageErr := botValidatePage(args.Page, args.PageSize)
			if pageErr != nil {
				return "", pageErr
			}
			items, total, err := s.issues.ListComments(args.IssueID, userID, page, pageSize)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimPage("comments", items, total, page, pageSize))
		default:
			page, pageSize, pageErr := botValidatePage(args.Page, args.PageSize)
			if pageErr != nil {
				return "", pageErr
			}
			items, total, err := s.issues.ListTimeline(args.IssueID, userID, page, pageSize)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimPage("timeline", items, total, page, pageSize))
		}
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_issue 不支持该读取动作")
}

// botSlimIssues 列表投影：保工具决策所需的最小字段。
func botSlimIssues(items []IssueResponse) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		out = append(out, botSlimIssue(&items[i]))
	}
	return out
}

func botSlimIssue(i *IssueResponse) map[string]interface{} {
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

// runIssueWrite 在 worker 内执行 fs_issue 写操作。
func (s *BotService) runIssueWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	switch call.Action {
	case "create":
		req := CreateInternalIssueRequest{
			Title: strings.TrimSpace(args.Title),
			Body:  args.Body,
		}
		source := strings.TrimSpace(args.Source)
		if source == "" || source == "internal" {
			return s.issues.CreateInternalIssue(args.ProjectID, userID, req)
		}
		if source == "github" {
			return s.issues.CreateGitHubIssueContext(ctx, args.ProjectID, userID, req)
		}
		return nil, errs.New(errs.ErrInvalidParams.Code, "source 只支持 internal/github")
	case "update":
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
		return s.issues.UpdateInternalIssueContext(ctx, args.IssueID, userID, req)
	case "set_workflow":
		status := model.IssueWorkflowStatus(strings.TrimSpace(args.WorkflowStatus))
		return s.issues.UpdateInternalMeta(args.IssueID, userID, status, botIssueActor)
	case "replace_checklist":
		req := ReplaceIssueChecklistRequest{Items: args.Items}
		return s.issues.ReplaceChecklist(args.IssueID, userID, req, botIssueActor)
	case "comment":
		req := CreateInternalIssueCommentRequest{Body: args.CommentBody}
		// 复用已有幂等入口；幂等键是服务端调用 ID，不让模型指定。
		return s.issues.CreateInternalCommentIdempotentContext(ctx, args.IssueID, userID, req, botIssueActor, call.ID)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "fs_issue 不支持该写动作")
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
		projectID, err := s.issueProjectOf(rawString(raw["issue_id"]), bot.UserID)
		if err != nil {
			// 找不到目标：保持空 scope，执行阶段会以同样错误失败；
			// 这里不建卡也不伪造范围。
			prepared.ScopeID = ""
			return
		}
		prepared.ScopeID = projectID
	}
	if prepared.Action == "update" {
		prepared.FieldsConstraint = presentFields(raw, "title", "body", "state", "state_reason", "labels")
	}
}

// issueProjectOf 读取 issue 归属项目（同时校验归属人）。
func (s *BotService) issueProjectOf(issueID, userID string) (string, error) {
	issue, err := s.issues.Get(issueID, userID)
	if err != nil {
		return "", err
	}
	return issue.ProjectId, nil
}

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
	// 外部文本字段进不可信围栏（INT-136）：nonce 为空（未启用）时按原样投影。
	// 读不到 nonce 就失败：围栏是安全保护，不能静默降级成裸文本。
	nonce, err := s.botNonceOf(call.BotID)
	if err != nil {
		return "", err
	}
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
		return botMarshalResult(botSlimPage("issues", botSlimIssues(items, nonce), total, page, pageSize))
	case "get", "comments", "timeline":
		switch call.Action {
		case "get":
			issue, err := s.issues.Get(args.IssueID, userID)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimIssueDetail(issue, nonce))
		case "comments":
			page, pageSize, pageErr := botValidatePage(args.Page, args.PageSize)
			if pageErr != nil {
				return "", pageErr
			}
			items, total, err := s.issues.ListComments(args.IssueID, userID, page, pageSize)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimPage("comments", botSlimIssueComments(items, nonce), total, page, pageSize))
		default:
			page, pageSize, pageErr := botValidatePage(args.Page, args.PageSize)
			if pageErr != nil {
				return "", pageErr
			}
			items, total, err := s.issues.ListTimeline(args.IssueID, userID, page, pageSize)
			if err != nil {
				return "", err
			}
			return botMarshalResult(botSlimPage("timeline", botSlimIssueTimeline(items, nonce), total, page, pageSize))
		}
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_issue 不支持该读取动作")
}

// botSlimIssues 列表投影：保工具决策所需的最小字段。
func botSlimIssues(items []IssueResponse, nonce string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		out = append(out, botSlimIssueListItem(&items[i], nonce))
	}
	return out
}

// botSlimIssueListItem 是列表项的精简形状：列表保持轻量，详情走 get。
// title 是外部内容（GitHub 同步或用户填写），进不可信围栏（INT-136）。
func botSlimIssueListItem(i *IssueResponse, nonce string) map[string]interface{} {
	out := map[string]interface{}{
		"id":         i.Id,
		"seq":        i.SequenceNumber,
		"title":      botFenceUntrusted(nonce, "issue_title", i.Title),
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
// 是后续写操作的写入保护基准，必须原样给出。title/body 是外部内容，进不
// 可信围栏（INT-136）。
func botSlimIssueDetail(i *IssueResponse, nonce string) map[string]interface{} {
	out := botSlimIssueListItem(i, nonce)
	out["reference"] = i.Reference
	out["body"] = botFenceUntrusted(nonce, "issue_body", i.Body)
	out["state_reason"] = i.StateReason
	out["author"] = i.Author.Login
	out["updated_at"] = i.UpdatedAt
	out["created_at"] = i.CreatedAt
	out["labels"] = []api.IssueLabel{}
	out["checklist"] = []api.IssueChecklistItem{}
	if i.InternalMeta != nil {
		if len(i.InternalMeta.Labels) > 0 {
			out["labels"] = botFenceIssueLabels(i.InternalMeta.Labels, nonce)
		}
		if len(i.InternalMeta.Checklist) > 0 {
			out["checklist"] = botFenceIssueChecklist(i.InternalMeta.Checklist, nonce)
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

// botFenceIssueLabels 围栏标签名与描述（INT-136）：标签由 GitHub 同步写入，
// 与 Issue 正文同一注入信道（仓库协作者可写）；颜色原样保留。
func botFenceIssueLabels(labels []api.IssueLabel, nonce string) []api.IssueLabel {
	if nonce == "" {
		return labels
	}
	out := make([]api.IssueLabel, 0, len(labels))
	for _, label := range labels {
		label.Name = botFenceUntrusted(nonce, "issue_label", label.Name)
		if label.Description != "" {
			label.Description = botFenceUntrusted(nonce, "issue_label", label.Description)
		}
		out = append(out, label)
	}
	return out
}

// botFenceIssueChecklist 围栏清单标题（INT-136）：清单整组替换、内容可经外部
// 内容诱导填写，与正文同属上下文里的未标记文本。
func botFenceIssueChecklist(items []api.IssueChecklistItem, nonce string) []api.IssueChecklistItem {
	if nonce == "" {
		return items
	}
	out := make([]api.IssueChecklistItem, 0, len(items))
	for _, item := range items {
		item.Title = botFenceUntrusted(nonce, "issue_checklist", item.Title)
		out = append(out, item)
	}
	return out
}

// botSlimIssueComments 是 comments 的精简投影：只给模型决策所需的字段，
// 不再整包透出 api.IssueComment；body 是外部内容，进不可信围栏（INT-136）。
func botSlimIssueComments(items []IssueCommentResponse, nonce string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		c := &items[i]
		out = append(out, map[string]interface{}{
			"id":         c.Id,
			"source":     string(c.Source),
			"author":     c.Author.Login,
			"body":       botFenceUntrusted(nonce, "issue_comment", c.Body),
			"created_at": c.CreatedAt,
		})
	}
	return out
}

// botSlimIssueComment 是单条评论的精简投影（写回执用），与列表同一形状。
func botSlimIssueComment(c *IssueCommentResponse, nonce string) map[string]interface{} {
	items := botSlimIssueComments([]IssueCommentResponse{*c}, nonce)
	return items[0]
}

// botFenceIssueMeta 围栏 Issue 元数据响应里的标签/清单（set_workflow /
// replace_checklist 的回执会带回它们，与外部内容同源）；其余字段原样保留。
func botFenceIssueMeta(meta *IssueInternalMetaResponse, nonce string) IssueInternalMetaResponse {
	if meta == nil || nonce == "" {
		if meta == nil {
			return IssueInternalMetaResponse{}
		}
		return *meta
	}
	out := *meta
	out.Labels = botFenceIssueLabels(meta.Labels, nonce)
	out.Checklist = botFenceIssueChecklist(meta.Checklist, nonce)
	return out
}

// botSlimIssueTimeline 是 timeline 的精简投影：不再整包透出
// api.IssueTimelineEvent；summary/body 是外部内容，进不可信围栏（INT-136）。
// 空串字段省略，投影保持轻量。
func botSlimIssueTimeline(items []IssueTimelineEventResponse, nonce string) []map[string]interface{} {
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
			item["summary"] = botFenceUntrusted(nonce, "issue_timeline", e.Summary)
		}
		if e.Body != "" {
			item["body"] = botFenceUntrusted(nonce, "issue_timeline", e.Body)
		}
		out = append(out, item)
	}
	return out
}

// botIssueConflictResult 把「他人已改动，本次未执行」回给模型：带上目标最新
// 内容，便于重新读取并保留双方修改后再提交，而不是静默覆盖。current 与
// get 同一投影，外部字段同样进围栏（INT-136）。
func botIssueConflictResult(reason string, current *IssueResponse, nonce string) map[string]interface{} {
	return map[string]interface{}{
		"conflict": true,
		"reason":   reason,
		"note":     "目标已被他人改动，本次未执行；请基于下面的最新内容重新读取，保留双方修改后再提交",
		"current":  botSlimIssueDetail(current, nonce),
	}
}

// runIssueWrite 在 worker 内执行 fs_issue 写操作。
func (s *BotService) runIssueWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botIssueArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	// 写回执也会带上 Issue 的标题/正文/标签/清单（外部可写的同一批字段），
	// 它们经「平台后台结果」进模型上下文，必须与读路径同一投影（INT-136）。
	// 读不到 nonce 就失败：结果会固化进账本，事后不补围栏。
	nonce, nonceErr := s.botNonceOf(call.BotID)
	if nonceErr != nil {
		return nil, nonceErr
	}
	switch call.Action {
	case "create":
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
			return botSlimIssueDetail(created, nonce), nil
		}
		if source == "github" {
			created, err := s.issues.CreateGitHubIssueContext(ctx, args.ProjectID, userID, req)
			if err != nil {
				return nil, err
			}
			return botSlimIssueDetail(created, nonce), nil
		}
		return nil, errs.New(errs.ErrInvalidParams.Code, "source 只支持 internal/github")
	case "update":
		// 写入保护：确认期间目标被别人改过就不执行，回冲突与最新内容。
		if expected := strings.TrimSpace(args.ExpectedUpdatedAt); expected != "" {
			current, err := s.issues.Get(args.IssueID, userID)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(current.UpdatedAt) != expected {
				return botIssueConflictResult("issue_updated", current, nonce), nil
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
		return botSlimIssueDetail(updated, nonce), nil
	case "set_workflow":
		status := model.IssueWorkflowStatus(strings.TrimSpace(args.WorkflowStatus))
		meta, err := s.issues.UpdateInternalMeta(args.IssueID, userID, status, botIssueActor)
		if err != nil {
			return nil, err
		}
		return botFenceIssueMeta(meta, nonce), nil
	case "replace_checklist":
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
				return botIssueConflictResult("checklist_updated", current, nonce), nil
			}
		}
		req := ReplaceIssueChecklistRequest{Items: args.Items}
		meta, err := s.issues.ReplaceChecklist(args.IssueID, userID, req, botIssueActor)
		if err != nil {
			return nil, err
		}
		return botFenceIssueMeta(meta, nonce), nil
	case "comment":
		req := CreateInternalIssueCommentRequest{Body: args.CommentBody}
		// 复用已有幂等入口；幂等键是服务端调用 ID，不让模型指定。
		comment, err := s.issues.CreateInternalCommentIdempotentContext(ctx, args.IssueID, userID, req, botIssueActor, call.ID)
		if err != nil {
			return nil, err
		}
		return botSlimIssueComment(comment, nonce), nil
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

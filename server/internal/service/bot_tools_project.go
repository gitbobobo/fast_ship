package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// fs_project 适配：读取直连 ProjectService；写操作由后台 worker 执行。
// 首期 create/update 只开放名称、描述、仓库 URL，不接受 token 引用或密钥。

func (s *BotService) execProjectRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	userID := s.botUserIDOf(call.BotID)
	switch args.Action {
	case "list":
		page, pageSize, err := botValidatePage(args.Page, args.PageSize)
		if err != nil {
			return "", err
		}
		items, total, err := s.projects.List(userID, page, pageSize)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimPage("projects", items, total, page, pageSize))
	case "get":
		if strings.TrimSpace(args.ID) == "" {
			return "", errs.ErrInvalidParams
		}
		project, err := s.projects.Get(args.ID, userID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimProject(project))
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_project 不支持该读取动作")
}

// runProjectWrite 在 worker 内执行 fs_project 写操作（确认/授权已完成）。
func (s *BotService) runProjectWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	userID := s.botUserIDOf(call.BotID)
	switch args.Action {
	case "create":
		req := &CreateProjectRequest{
			Name:        strings.TrimSpace(args.Name),
			Description: args.Description,
		}
		if args.RepositoryURL != "" {
			req.RepositoryUrl = &args.RepositoryURL
		}
		return s.projects.Create(userID, req)
	case "update":
		req := &UpdateProjectRequest{Description: args.Description}
		if name := strings.TrimSpace(args.Name); name != "" {
			req.Name = &name
		}
		if args.RepositoryURL != "" {
			req.RepositoryUrl = &args.RepositoryURL
		}
		return s.projects.Update(args.ID, userID, req)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "fs_project 不支持该写动作")
}

// resolveProjectScope 在建卡前解析写操作的范围与字段约束。
func (s *BotService) resolveProjectScope(bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage) {
	switch prepared.Action {
	case "update":
		prepared.ScopeKind = botScopeProject
		prepared.ScopeID = rawString(raw["id"])
		prepared.FieldsConstraint = presentFields(raw, "name", "description", "repository_url")
	case "create":
		// 项目创建的范围是当前用户的项目集合。
		prepared.ScopeKind = botScopeProjectCollection
		prepared.ScopeID = bot.UserID
	}
}

func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return strings.TrimSpace(s)
}

// presentFields 返回 raw 中实际出现的字段名（授权字段约束）。
func presentFields(raw map[string]json.RawMessage, keys ...string) []string {
	out := []string{}
	for _, key := range keys {
		if v, ok := raw[key]; ok && string(v) != "null" {
			out = append(out, key)
		}
	}
	return out
}

// 授权范围类型。
const (
	botScopeProject           = "project"
	botScopeProjectCollection = "project_collection"
	botScopeIssueProject      = "issue_project"
	botScopeThreadLaunch      = "thread_launch"
	botScopeThread            = "thread"
	botScopeThreadStop        = "thread_stop"
)

// botValidatePage 校验分页参数：工具适配层不照搬 HTTP handler 的假设。
func botValidatePage(page, pageSize int) (int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		return 0, 0, errs.New(errs.ErrInvalidParams.Code, "page_size 最大 100")
	}
	return page, pageSize, nil
}

// botMarshalResult 序列化工具结果；失败按内部错误处理。
func botMarshalResult(v interface{}) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", errs.ErrInternal
	}
	return string(raw), nil
}

// botSlimPage 统一列表结果的精简形状。
func botSlimPage[T any](kind string, items []T, total int64, page, pageSize int) map[string]interface{} {
	return map[string]interface{}{
		"kind":      kind,
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}
}

// botSlimProject 投影为工具专用精简对象（不含 token 引用等敏感细节）。
func botSlimProject(p *ProjectResponse) map[string]interface{} {
	out := map[string]interface{}{
		"id":   p.Id,
		"name": p.Name,
	}
	if p.Description != "" {
		out["description"] = p.Description
	}
	if p.GithubOwner != "" || p.GithubRepo != "" {
		out["github_repo"] = p.GithubOwner + "/" + p.GithubRepo
	}
	return out
}

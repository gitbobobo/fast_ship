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
// 动作注册表 spec 组见文件末尾（INT-195）。

func (s *BotService) execProjectList(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	page, pageSize, err := botValidatePage(args.Page, args.PageSize)
	if err != nil {
		return "", err
	}
	// 默认排除搁置中的项目（INT-165）：项目清单是「接下来操作哪个」的
	// 挑选面；include_shelved=true 时连搁置项目一起返回（总数字段同口径）。
	items, total, err := s.projects.List(s.botUserIDOf(call.BotID), page, pageSize,
		args.IncludeShelved != nil && *args.IncludeShelved)
	if err != nil {
		return "", err
	}
	// 与 get 一致走瘦身投影：完整响应带 GitHub 凭证引用 ID，不进模型上下文。
	slim := make([]map[string]interface{}, 0, len(items))
	for i := range items {
		slim = append(slim, botSlimProject(&items[i]))
	}
	return botMarshalResult(botSlimPage("projects", slim, total, page, pageSize))
}

func (s *BotService) execProjectGet(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	if strings.TrimSpace(args.ID) == "" {
		return "", errs.ErrInvalidParams
	}
	project, err := s.projects.Get(args.ID, s.botUserIDOf(call.BotID))
	if err != nil {
		return "", err
	}
	return botMarshalResult(botSlimProject(project))
}

// runProjectCreate/runProjectUpdate 在 worker 内执行 fs_project 写操作
// （确认/授权已完成；注册表 AsyncExec 直接指向单动作方法）。结果与读取一致
// 走瘦身投影：完整响应带凭证引用，会随后台结果发给模型供应商。
func (s *BotService) runProjectCreate(_ context.Context, call *model.BotToolCall) (interface{}, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	userID := s.botUserIDOf(call.BotID)
	req := &CreateProjectRequest{
		Name:        strings.TrimSpace(args.Name),
		Description: args.Description,
	}
	if args.RepositoryURL != "" {
		req.RepositoryUrl = &args.RepositoryURL
	}
	resp, err := s.projects.Create(userID, req)
	if err != nil {
		return nil, err
	}
	return botSlimProject(resp), nil
}

func (s *BotService) runProjectUpdate(_ context.Context, call *model.BotToolCall) (interface{}, error) {
	var args botProjectArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	userID := s.botUserIDOf(call.BotID)
	req := &UpdateProjectRequest{Description: args.Description}
	if name := strings.TrimSpace(args.Name); name != "" {
		req.Name = &name
	}
	if args.RepositoryURL != "" {
		req.RepositoryUrl = &args.RepositoryURL
	}
	resp, err := s.projects.Update(args.ID, userID, req)
	if err != nil {
		return nil, err
	}
	return botSlimProject(resp), nil
}

// botProjectActionSpecs 是 fs_project 的动作声明（INT-195）：范围解析与策略
// 档位随 spec 声明；scope 解析的细节在 resolveProjectScope。测试驱动写执行体
// 用测试侧的 botProjectWriteTestExec（生产分派走 runAsyncTool）。
func botProjectActionSpecs() []*botActionSpec {
	scope := func(s *BotService, bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage, _ *botCallExecContext) error {
		s.resolveProjectScope(bot, prepared, raw)
		return nil
	}
	return []*botActionSpec{
		{
			Tool: "fs_project", Action: "list",
			Description: "列出当前用户项目（默认排除搁置中项目；include_shelved=true 时包含）",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			SyncExec: (*BotService).execProjectList,
		},
		{
			Tool: "fs_project", Action: "get",
			Description: "读取项目详情",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("id"), Forbidden: []string{"include_shelved"},
			SyncExec: (*BotService).execProjectGet,
		},
		{
			Tool: "fs_project", Action: "create",
			Description: "创建项目",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways, PolicyTier: "tier2",
			Required: req("name"), Forbidden: []string{"id", "include_shelved"},
			AsyncExec: (*BotService).runProjectCreate,
			Scope:     scope,
		},
		{
			Tool: "fs_project", Action: "update",
			Description: "更新项目（名称/描述/仓库 URL）",
			Mode:        model.BotExecutionAsync, Confirmation: confirmAlways, PolicyTier: "tier2",
			Required: req("id"), Forbidden: []string{"include_shelved"},
			AsyncExec: (*BotService).runProjectUpdate,
			Scope:     scope,
		},
	}
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
	// botScopeThreadSettle 结算/撤销结算（settle/unsettle），botScopeThreadQueue
	// 恢复队列（resume_queue）：都是按线程粒度的写操作，scope kind 只进授权
	// 规则哈希与确认卡片展示。
	botScopeThreadSettle = "thread_settle"
	botScopeThreadQueue  = "thread_queue"
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
// 搁置三字段恒带（INT-165）：is_shelved 按响应生成时刻判定，未搁置时两个
// 时间为 null——模型据此区分「没搁置」与「搁置已到期」。
func botSlimProject(p *ProjectResponse) map[string]interface{} {
	out := map[string]interface{}{
		"id":            p.Id,
		"name":          p.Name,
		"shelved_at":    p.ShelvedAt,
		"shelved_until": p.ShelvedUntil,
		"is_shelved":    p.IsShelved,
	}
	if p.Description != "" {
		out["description"] = p.Description
	}
	if p.GithubOwner != "" || p.GithubRepo != "" {
		out["github_repo"] = p.GithubOwner + "/" + p.GithubRepo
	}
	return out
}

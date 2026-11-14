package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// MCP 服务（INT-222）：/mcp Streamable HTTP 端点的工具层。每个已鉴权请求
// 构造独立的 mcpsdk.Server（工具闭包绑定该请求的用户与 scope），由
// mcpsdk.NewStreamableHTTPHandler 的 per-request factory 调用；Bearer 鉴权
// 挑战在 middleware.RequireMCPToken 完成。
//
// 工具全部以授权用户身份走 IssueService（与 JWT 同一条链，含归属校验），
// 不登记进机器人动作注册表、不接入机器人工具循环。
// 工具命名用 fastship_ 前缀：工具名字符集（字母/数字/_-.）允许下划线，
// 与模块语义 fastship 保持同源可读。

const (
	// MCPServerName MCP server 实现名（initialize 响应的 serverInfo.name）。
	MCPServerName = "fast-ship"
	// mcpServerVersion serverInfo.version；站内无全局版本常量，此处独立维护。
	mcpServerVersion = "1.0.0"

	// mcpToolPrefix 第一期工具命名前缀。
	mcpToolPrefix = "fastship_"
)

// ---- scope 强制 ----

// mcpCheckScope 工具层 scope 判定：读工具要求 read（write 隐含 read），
// 写工具要求 write。项目从工具入参或 issue 归属解析。
func mcpCheckScope(scopes, projectID string, write bool) error {
	if write {
		if !ScopeHasWrite(scopes, projectID) {
			return fmt.Errorf("缺少项目 %s 的写入权限（需要 scope project:%s:write）", projectID, projectID)
		}
		return nil
	}
	if !ScopeHasRead(scopes, projectID) {
		return fmt.Errorf("缺少项目 %s 的读取权限（需要 scope project:%s:read）", projectID, projectID)
	}
	return nil
}

// mcpServiceError 把 IssueService 的 AppError 归类成工具层文案；内部错误
// 不外泄细节。
func mcpServiceError(err error) error {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		if appErr.Code == errs.ErrInternal.Code {
			return errors.New("服务器内部错误")
		}
		return errors.New(appErr.Message)
	}
	return errors.New("服务器内部错误")
}

// ---- 请求 context 携带的令牌信息 ----

type mcpAuthKey struct{}

// ContextWithMCPAuth 把校验通过的 access token 信息注入 context
// （middleware.RequireMCPToken 调用）。
func ContextWithMCPAuth(ctx context.Context, info *OAuthAccessTokenInfo) context.Context {
	return context.WithValue(ctx, mcpAuthKey{}, info)
}

// MCPAuthFromContext 取出当前请求的令牌信息；未鉴权返回 nil。
func MCPAuthFromContext(ctx context.Context) *OAuthAccessTokenInfo {
	info, _ := ctx.Value(mcpAuthKey{}).(*OAuthAccessTokenInfo)
	return info
}

// ---- 工具入参 ----

type mcpListIssuesIn struct {
	ProjectID string `json:"project_id" jsonschema:"项目 ID（UUID），必填"`
	// State open / closed；空串表示全部（与 /api/projects/:id/issues 的 state 过滤同语义）。
	State    string `json:"state,omitempty" jsonschema:"按状态过滤：open 或 closed，缺省为全部"`
	Query    string `json:"q,omitempty" jsonschema:"关键词搜索标题与正文"`
	Label    string `json:"label,omitempty" jsonschema:"按标签名过滤"`
	Page     int    `json:"page,omitempty" jsonschema:"页码，从 1 开始，缺省 1"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"每页条数（1-100），缺省 20"`
}

type mcpGetIssueIn struct {
	IssueID string `json:"issue_id" jsonschema:"Issue ID（UUID），必填"`
}

type mcpCreateIssueIn struct {
	ProjectID      string `json:"project_id" jsonschema:"项目 ID（UUID），必填"`
	Title          string `json:"title" jsonschema:"Issue 标题，trim 后须非空"`
	Body           string `json:"body,omitempty" jsonschema:"Issue 正文（Markdown），可选"`
	WorkflowStatus string `json:"workflow_status,omitempty" jsonschema:"工作流状态：todo / in_progress / done，可选"`
}

type mcpUpdateIssueIn struct {
	IssueID string   `json:"issue_id" jsonschema:"Issue ID（UUID），必填"`
	Title   string   `json:"title,omitempty" jsonschema:"新标题；缺省不修改"`
	Body    string   `json:"body,omitempty" jsonschema:"新正文（Markdown），整体替换；缺省不修改"`
	Labels  []string `json:"labels,omitempty" jsonschema:"标签名数组，整体替换；缺省不修改"`
}

type mcpCloseIssueIn struct {
	IssueID string `json:"issue_id" jsonschema:"Issue ID（UUID），必填"`
	// Reason completed / not_planned；缺省不设 reason（服务端按现状处理）。
	Reason string `json:"reason,omitempty" jsonschema:"关闭原因：completed 或 not_planned，可选"`
}

type mcpReopenIssueIn struct {
	IssueID string `json:"issue_id" jsonschema:"Issue ID（UUID），必填"`
}

type mcpAddCommentIn struct {
	IssueID string `json:"issue_id" jsonschema:"Issue ID（UUID），必填"`
	Body    string `json:"body" jsonschema:"评论正文（Markdown），trim 后须非空"`
}

// ---- 纯参数构造（单测覆盖点） ----

// mcpListParams 归一化 list 入参：page/page_size 夹取到合法区间。
func mcpListParams(in mcpListIssuesIn) (IssueListFilters, int, int) {
	state := strings.TrimSpace(in.State)
	filters := IssueListFilters{
		State: state,
		Query: in.Query,
		Label: in.Label,
	}
	page := in.Page
	if page < 1 {
		page = 1
	}
	pageSize := in.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return filters, page, pageSize
}

// mcpUpdateRequest 从入参构造 issue 更新请求；全空（没有任何要改的字段）
// 返回错误。
func mcpUpdateRequest(in mcpUpdateIssueIn) (UpdateInternalIssueRequest, error) {
	req := UpdateInternalIssueRequest{}
	changed := false
	if title := strings.TrimSpace(in.Title); title != "" {
		req.Title = &title
		changed = true
	}
	if in.Body != "" {
		body := in.Body
		req.Body = &body
		changed = true
	}
	if len(in.Labels) > 0 {
		req.Labels = in.Labels
		changed = true
	}
	if !changed {
		return req, errors.New("未提供任何要修改的字段（title / body / labels 至少一项）")
	}
	return req, nil
}

// mcpCloseRequest 关闭请求：state=closed，reason 仅接受 completed/not_planned。
func mcpCloseRequest(in mcpCloseIssueIn) (UpdateInternalIssueRequest, error) {
	state := model.IssueStateClosed
	req := UpdateInternalIssueRequest{State: &state}
	if reason := strings.TrimSpace(in.Reason); reason != "" {
		if reason != "completed" && reason != "not_planned" {
			return req, errors.New("reason 仅接受 completed 或 not_planned")
		}
		value := reason
		req.StateReason = (*api.UpdateIssueRequestStateReason)(&value)
	}
	return req, nil
}

// mcpReopenRequest 重开请求：state=open（reason 置空即清空 state_reason）。
func mcpReopenRequest() UpdateInternalIssueRequest {
	state := model.IssueStateOpen
	return UpdateInternalIssueRequest{State: &state}
}

// ---- 投影 ----

// slimIssue 工具响应的精简投影：不携带 body_html、标注、PR 列表等 Web 专有
// 大字段。标签取 internal meta（内建）或 github meta（镜像）两处来源。
func slimIssue(resp *IssueResponse) map[string]any {
	if resp == nil {
		return nil
	}
	labels := []string{}
	if resp.InternalMeta != nil {
		for _, l := range resp.InternalMeta.Labels {
			labels = append(labels, l.Name)
		}
	}
	if resp.Github != nil {
		for _, l := range resp.Github.Labels {
			labels = append(labels, l.Name)
		}
	}
	workflowStatus := ""
	if resp.InternalMeta != nil {
		workflowStatus = string(resp.InternalMeta.WorkflowStatus)
	}
	return map[string]any{
		"id":              resp.Id,
		"reference":       resp.Reference,
		"project_id":      resp.ProjectId,
		"title":           resp.Title,
		"state":           resp.State,
		"state_reason":    resp.StateReason,
		"source":          resp.Source,
		"author":          resp.Author.Login,
		"labels":          labels,
		"workflow_status": workflowStatus,
		"body":            resp.Body,
		"created_at":      resp.CreatedAt,
		"updated_at":      resp.UpdatedAt,
		"closed_at":       resp.ClosedAt,
	}
}

func slimIssues(items []IssueResponse) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, slimIssue(&items[i]))
	}
	return out
}

// ---- MCPService ----

// MCPService /mcp 端点的工具宿主：按请求构造绑定用户 scope 的 mcpsdk.Server。
type MCPService struct {
	issues *IssueService
}

// NewMCPService 构造 MCPService；工具全部以授权用户身份走 IssueService。
func NewMCPService(issues *IssueService) *MCPService {
	return &MCPService{issues: issues}
}

// NewServer 为单个已鉴权请求构造 MCP server（StreamableHTTPHandler 的
// per-request factory 调用；auth 由 middleware 注入请求 context）。
func (s *MCPService) NewServer(auth *OAuthAccessTokenInfo) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: MCPServerName, Version: mcpServerVersion}, nil)
	s.registerTools(server, auth)
	return server
}

// registerTools 登记第一期工具。闭包持有本次请求的 scopes/userID，实现
// 不同用户间的 scope 隔离。
func (s *MCPService) registerTools(server *mcpsdk.Server, auth *OAuthAccessTokenInfo) {
	scopes, userID := "", ""
	if auth != nil {
		scopes, userID = auth.Scopes, auth.UserID
	}

	// resolveIssue 解析 issue（含归属校验）并对归属项目做 scope 判定；
	// scope 不满足时返回工具错误，不触达写路径。
	resolveIssue := func(issueID string, write bool) (*IssueResponse, error) {
		resp, err := s.issues.Get(issueID, userID)
		if err != nil {
			return nil, mcpServiceError(err)
		}
		if err := mcpCheckScope(scopes, resp.ProjectId, write); err != nil {
			return nil, err
		}
		return resp, nil
	}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "list_issues",
		Description: "列出指定 Fast Ship 项目的 Issue（分页；支持状态/关键词/标签过滤）",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpListIssuesIn) (*mcpsdk.CallToolResult, any, error) {
		if err := mcpCheckScope(scopes, in.ProjectID, false); err != nil {
			return nil, nil, err
		}
		filters, page, pageSize := mcpListParams(in)
		items, total, err := s.issues.List(ctx, in.ProjectID, userID, filters, page, pageSize)
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, map[string]any{"total": total, "issues": slimIssues(items)}, nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "get_issue",
		Description: "获取单个 Issue 详情（标题、正文、状态、标签等）",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpGetIssueIn) (*mcpsdk.CallToolResult, any, error) {
		resp, err := resolveIssue(in.IssueID, false)
		if err != nil {
			return nil, nil, err
		}
		return nil, slimIssue(resp), nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "create_issue",
		Description: "在指定 Fast Ship 项目创建 internal Issue",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpCreateIssueIn) (*mcpsdk.CallToolResult, any, error) {
		if err := mcpCheckScope(scopes, in.ProjectID, true); err != nil {
			return nil, nil, err
		}
		createReq := api.CreateIssueRequest{Title: strings.TrimSpace(in.Title)}
		if in.Body != "" {
			body := in.Body
			createReq.Body = &body
		}
		if ws := strings.TrimSpace(in.WorkflowStatus); ws != "" {
			status := model.IssueWorkflowStatus(ws)
			createReq.WorkflowStatus = &status
		}
		resp, err := s.issues.CreateInternalIssue(in.ProjectID, userID, createReq)
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, slimIssue(resp), nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "update_issue",
		Description: "更新 Issue（标题/正文/标签，缺省字段不修改）",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpUpdateIssueIn) (*mcpsdk.CallToolResult, any, error) {
		updateReq, err := mcpUpdateRequest(in)
		if err != nil {
			return nil, nil, err
		}
		if _, err := resolveIssue(in.IssueID, true); err != nil {
			return nil, nil, err
		}
		resp, err := s.issues.UpdateInternalIssueContext(ctx, in.IssueID, userID, updateReq)
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, slimIssue(resp), nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "close_issue",
		Description: "关闭 Issue（reason 可选 completed / not_planned）",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpCloseIssueIn) (*mcpsdk.CallToolResult, any, error) {
		updateReq, err := mcpCloseRequest(in)
		if err != nil {
			return nil, nil, err
		}
		if _, err := resolveIssue(in.IssueID, true); err != nil {
			return nil, nil, err
		}
		resp, err := s.issues.UpdateInternalIssueContext(ctx, in.IssueID, userID, updateReq)
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, slimIssue(resp), nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "reopen_issue",
		Description: "重开已关闭的 Issue",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpReopenIssueIn) (*mcpsdk.CallToolResult, any, error) {
		if _, err := resolveIssue(in.IssueID, true); err != nil {
			return nil, nil, err
		}
		resp, err := s.issues.UpdateInternalIssueContext(ctx, in.IssueID, userID, mcpReopenRequest())
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, slimIssue(resp), nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        mcpToolPrefix + "add_comment",
		Description: "为 Issue 发表评论（Markdown 正文）",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in mcpAddCommentIn) (*mcpsdk.CallToolResult, any, error) {
		if _, err := resolveIssue(in.IssueID, true); err != nil {
			return nil, nil, err
		}
		comment, err := s.issues.CreateInternalComment(in.IssueID, userID,
			api.CreateIssueCommentJSONBody{Body: in.Body}, "MCP")
		if err != nil {
			return nil, nil, mcpServiceError(err)
		}
		return nil, map[string]any{
			"id":         comment.Id,
			"issue_id":   comment.IssueId,
			"body":       comment.Body,
			"created_at": comment.CreatedAt,
		}, nil
	})
}

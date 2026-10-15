package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// AutomationAgentHandler 自动化运行令牌专用端点（INT-90）。
// 路由由 middleware.RequireRunToken 守卫（见 router.go）——本 handler 只在
// 已解析出 user_id/run_id 的上下文里执行。

type AutomationAgentHandler struct {
	agentService *service.AutomationAgentService
}

func NewAutomationAgentHandler(agentService *service.AutomationAgentService) *AutomationAgentHandler {
	return &AutomationAgentHandler{agentService: agentService}
}

// Agent 暴露令牌校验服务，供 router 挂 RequireRunToken 中间件。
func (h *AutomationAgentHandler) Agent() *service.AutomationAgentService {
	return h.agentService
}

// Report POST /api/automation-agent/report
func (h *AutomationAgentHandler) Report(c *gin.Context) {
	var req api.AutomationRunReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	identity := &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
	result, err := h.agentService.Report(c.Request.Context(), identity, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ListThreads GET /api/automation-agent/threads
func (h *AutomationAgentHandler) ListThreads(c *gin.Context) {
	var settled *bool
	if raw := c.Query("settled"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, 40001, "settled 必须是布尔值")
			return
		}
		settled = &parsed
	}
	identity := &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
	result, err := h.agentService.ListThreads(c.Request.Context(), identity,
		strings.TrimSpace(c.Query("environment_id")),
		strings.TrimSpace(c.Query("project_id")),
		strings.TrimSpace(c.Query("status")),
		settled)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetThread GET /api/automation-agent/environments/:id/threads/:threadId
func (h *AutomationAgentHandler) GetThread(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	identity := &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
	result, err := h.agentService.ThreadDetail(c.Request.Context(), identity,
		c.Param("id"), c.Param("threadId"), limit, strings.TrimSpace(c.Query("before_item_id")))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetThreadItem GET /api/automation-agent/environments/:id/threads/:threadId/items/:itemId
func (h *AutomationAgentHandler) GetThreadItem(c *gin.Context) {
	identity := &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
	result, err := h.agentService.ThreadItemDetail(c.Request.Context(), identity,
		c.Param("id"), c.Param("threadId"), c.Param("itemId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// PublishNotification POST /api/automation-agent/notifications
func (h *AutomationAgentHandler) PublishNotification(c *gin.Context) {
	var req api.AgentNotificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	identity := &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
	if err := h.agentService.PublishNotification(c.Request.Context(), identity, &req); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ---- GitHub PR 评审代发（INT-93）：owner/repo/PR/SHA 全部取自运行行 ----

func (h *AutomationAgentHandler) identity(c *gin.Context) *service.RunTokenIdentity {
	return &service.RunTokenIdentity{
		UserID: middleware.GetUserID(c),
		RunID:  middleware.GetRunID(c),
	}
}

// GetPullRequest GET /api/automation-agent/pull-request
func (h *AutomationAgentHandler) GetPullRequest(c *gin.Context) {
	result, err := h.agentService.AgentPullRequest(c.Request.Context(), h.identity(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetPullRequestDiff GET /api/automation-agent/pull-request/diff
func (h *AutomationAgentHandler) GetPullRequestDiff(c *gin.Context) {
	result, err := h.agentService.AgentPullRequestDiff(c.Request.Context(), h.identity(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ListPullRequestComments GET /api/automation-agent/pull-request/comments
func (h *AutomationAgentHandler) ListPullRequestComments(c *gin.Context) {
	items, err := h.agentService.AgentPullRequestComments(c.Request.Context(), h.identity(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// SubmitPullRequestReview POST /api/automation-agent/pull-request/review
func (h *AutomationAgentHandler) SubmitPullRequestReview(c *gin.Context) {
	var req api.AgentPullRequestReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.agentService.AgentSubmitPullRequestReview(c.Request.Context(), h.identity(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// UpdatePullRequestCheck POST /api/automation-agent/pull-request/check
func (h *AutomationAgentHandler) UpdatePullRequestCheck(c *gin.Context) {
	var req api.AgentCheckUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	if err := h.agentService.AgentUpdateCheck(c.Request.Context(), h.identity(c), &req); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

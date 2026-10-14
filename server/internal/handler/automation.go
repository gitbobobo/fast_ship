package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// AutomationHandler 自动化管理面（INT-90，仅 JWT）：配置 CRUD 与运行记录查询。

type AutomationHandler struct {
	automationService *service.AutomationService
}

func NewAutomationHandler(automationService *service.AutomationService) *AutomationHandler {
	return &AutomationHandler{automationService: automationService}
}

func (h *AutomationHandler) List(c *gin.Context) {
	items, err := h.automationService.List(c.Request.Context(), middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *AutomationHandler) Create(c *gin.Context) {
	var req service.CreateAutomationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.automationService.Create(c.Request.Context(), middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AutomationHandler) Get(c *gin.Context) {
	result, err := h.automationService.Get(c.Request.Context(), middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AutomationHandler) Update(c *gin.Context) {
	var req service.UpdateAutomationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.automationService.Update(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AutomationHandler) Delete(c *gin.Context) {
	if err := h.automationService.Delete(c.Request.Context(), middleware.GetUserID(c), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

func (h *AutomationHandler) ListRuns(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	items, err := h.automationService.ListRuns(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *AutomationHandler) GetRun(c *gin.Context) {
	result, err := h.automationService.GetRun(c.Request.Context(), middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// NotificationHandler 站内通知（仅 JWT）。

type NotificationHandler struct {
	notificationService *service.NotificationService
}

func NewNotificationHandler(notificationService *service.NotificationService) *NotificationHandler {
	return &NotificationHandler{notificationService: notificationService}
}

func (h *NotificationHandler) List(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	unreadOnly := c.Query("unread_only") == "true"
	items, err := h.notificationService.List(c.Request.Context(), middleware.GetUserID(c), limit, unreadOnly)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	count, err := h.notificationService.UnreadCount(c.Request.Context(), middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"count": count})
}

func (h *NotificationHandler) MarkRead(c *gin.Context) {
	if err := h.notificationService.MarkRead(c.Request.Context(), middleware.GetUserID(c), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	if err := h.notificationService.MarkAllRead(c.Request.Context(), middleware.GetUserID(c)); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

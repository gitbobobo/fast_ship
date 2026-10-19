package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// NotificationHandler 站内通知（仅 JWT）。原属 automation.go，INT-135 起独立。
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

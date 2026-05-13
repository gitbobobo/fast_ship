package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type DashboardHandler struct {
	dashboardService *service.DashboardService
}

func NewDashboardHandler(dashboardService *service.DashboardService) *DashboardHandler {
	return &DashboardHandler{dashboardService: dashboardService}
}

func (h *DashboardHandler) Overview(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.dashboardService.GetOverview(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

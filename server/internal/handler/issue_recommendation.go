package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssueRecommendationHandler struct {
	recService *service.IssueRecommendationService
}

func NewIssueRecommendationHandler(recService *service.IssueRecommendationService) *IssueRecommendationHandler {
	return &IssueRecommendationHandler{recService: recService}
}

func (h *IssueRecommendationHandler) requireApiKey(c *gin.Context) bool {
	if middleware.IsJWTAuth(c) {
		middleware.HandleAppError(c, errs.ErrApiKeyRequired)
		return false
	}
	return true
}

// Upsert 写入或覆盖推荐；仅 API Key 可写，created_by 记录 API Key 名称。
func (h *IssueRecommendationHandler) Upsert(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req service.UpsertIssueRecommendationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.recService.Upsert(issueID, userID, middleware.GetAPIKeyName(c), req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueRecommendationHandler) Delete(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.recService.Delete(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

func (h *IssueRecommendationHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	projectID := c.Query("project_id")

	result, err := h.recService.List(userID, projectID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

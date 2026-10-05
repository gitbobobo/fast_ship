package handler

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type AIHandler struct {
	aiService *service.AIService
}

func NewAIHandler(aiService *service.AIService) *AIHandler {
	return &AIHandler{aiService: aiService}
}

func (h *AIHandler) GetSettings(c *gin.Context) {
	userID := middleware.GetUserID(c)

	result, err := h.aiService.GetSettings(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AIHandler) UpdateSettings(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req service.UpdateAISettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.aiService.UpdateSettings(userID, req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AIHandler) SuggestIssueChecklist(c *gin.Context) {
	userID := middleware.GetUserID(c)
	issueID := c.Param("iid")

	result, err := h.aiService.SuggestIssueChecklist(c.Request.Context(), issueID, userID, middleware.ActorLabel(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AIHandler) GenerateTitle(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req api.GenerateIssueTitleJSONBody
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	if utf8.RuneCountInString(strings.TrimSpace(req.Body)) < 10 {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.aiService.GenerateTitle(c.Request.Context(), req.Body, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

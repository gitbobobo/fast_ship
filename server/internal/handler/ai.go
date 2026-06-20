package handler

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type AIHandler struct {
	aiService *service.AIService
}

type updateAISettingsRequest struct {
	APIHost string `json:"api_host"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
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

	var req updateAISettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.aiService.UpdateSettings(userID, service.UpdateAISettingsRequest{
		APIHost: req.APIHost,
		APIKey:  req.APIKey,
		Model:   req.Model,
	})
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

type generateTitleRequest struct {
	Body string `json:"body"`
}

func (h *AIHandler) GenerateTitle(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req generateTitleRequest
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

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssuePromptHandler struct {
	service *service.IssuePromptService
}

func NewIssuePromptHandler(service *service.IssuePromptService) *IssuePromptHandler {
	return &IssuePromptHandler{service: service}
}

func (h *IssuePromptHandler) GetPrompts(c *gin.Context) {
	userID := middleware.GetUserID(c)

	result, err := h.service.GetPrompts(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssuePromptHandler) UpdatePrompts(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req service.UpdateIssuePromptsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.service.UpdatePrompts(userID, req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

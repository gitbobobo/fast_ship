package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssueCollabHandler struct {
	collabService *service.IssueCollabService
}

func NewIssueCollabHandler(collabService *service.IssueCollabService) *IssueCollabHandler {
	return &IssueCollabHandler{collabService: collabService}
}

func (h *IssueCollabHandler) requireApiKey(c *gin.Context) bool {
	if middleware.IsJWTAuth(c) {
		middleware.HandleAppError(c, errs.ErrApiKeyRequired)
		return false
	}
	return true
}

func (h *IssueCollabHandler) UpsertForKind(kind model.CollabDocumentKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.requireApiKey(c) {
			return
		}
		issueID := c.Param("iid")
		userID := middleware.GetUserID(c)

		var req struct {
			Body string `json:"body"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			middleware.HandleAppError(c, errs.ErrInvalidParams)
			return
		}

		result, err := h.collabService.Upsert(issueID, userID, model.CollabAuthorAgent, kind, service.UpsertIssueCollabRequest{Body: req.Body})
		if err != nil {
			middleware.HandleAppError(c, err)
			return
		}
		response.Success(c, result)
	}
}

func (h *IssueCollabHandler) DeleteForKind(kind model.CollabDocumentKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		issueID := c.Param("iid")
		userID := middleware.GetUserID(c)
		if err := h.collabService.Delete(issueID, userID, kind); err != nil {
			middleware.HandleAppError(c, err)
			return
		}
		response.Success(c, nil)
	}
}

func (h *IssueCollabHandler) GetArea(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	result, err := h.collabService.GetArea(issueID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) ClearArea(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.collabService.ClearArea(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
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

// requireApiKey 限定协作区 PUT 写操作仅 API Key；DELETE 不经此守卫。
func (h *IssueCollabHandler) requireApiKey(c *gin.Context) bool {
	if middleware.IsJWTAuth(c) {
		middleware.HandleAppError(c, errs.ErrApiKeyRequired)
		return false
	}
	return true
}

type replaceIssueCollabSuggestionsRequest struct {
	Items []service.IssueCollabSuggestionInput `json:"items"`
}

type upsertIssueCollabPlanRequest struct {
	Body string `json:"body"`
}

type upsertIssueCollabReviewRequest struct {
	Body string `json:"body"`
}

type upsertIssueCollabSummaryRequest struct {
	Body      string   `json:"body"`
	CommitIDs []string `json:"commit_ids"`
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

func (h *IssueCollabHandler) ReplaceSuggestions(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req replaceIssueCollabSuggestionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.ReplaceSuggestions(issueID, userID, authorKind, service.ReplaceIssueCollabSuggestionsRequest{Items: req.Items})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) UpsertPlan(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req upsertIssueCollabPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.UpsertPlan(issueID, userID, authorKind, service.UpsertIssueCollabPlanRequest{Body: req.Body})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) UpsertReview(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req upsertIssueCollabReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.UpsertReview(issueID, userID, authorKind, service.UpsertIssueCollabReviewRequest{Body: req.Body})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) UpsertSummary(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req upsertIssueCollabSummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.UpsertSummary(issueID, userID, authorKind, service.UpsertIssueCollabSummaryRequest{Body: req.Body, CommitIDs: req.CommitIDs})
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

func (h *IssueCollabHandler) ClearSuggestions(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.collabService.ClearSuggestions(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *IssueCollabHandler) DeletePlan(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.collabService.DeletePlan(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *IssueCollabHandler) DeleteReview(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.collabService.DeleteReview(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *IssueCollabHandler) DeleteSummary(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	if err := h.collabService.DeleteSummary(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

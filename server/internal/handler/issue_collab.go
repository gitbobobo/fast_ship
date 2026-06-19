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

// requireJWT 限定作答/背景等"人"的专属操作仅网页登录可用，API Key 调用返回 403。
func (h *IssueCollabHandler) requireJWT(c *gin.Context) bool {
	if !middleware.IsJWTAuth(c) {
		middleware.HandleAppError(c, errs.ErrApiKeyForbidden)
		return false
	}
	return true
}

type createIssueCollabNoteRequest struct {
	Body string `json:"body"`
}

type updateIssueCollabNoteRequest struct {
	Body string `json:"body"`
}

type createIssueCollabQuestionsRequest struct {
	Items []service.IssueCollabQuestionInput `json:"items"`
}

type answerIssueCollabQuestionRequest struct {
	Answer string `json:"answer"`
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

func (h *IssueCollabHandler) CreateNote(c *gin.Context) {
	if !h.requireJWT(c) {
		return
	}
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req createIssueCollabNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.CreateNote(issueID, userID, authorKind, service.CreateIssueCollabNoteRequest{Body: req.Body})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) UpdateNote(c *gin.Context) {
	if !h.requireJWT(c) {
		return
	}
	issueID := c.Param("iid")
	noteID := c.Param("nid")
	userID := middleware.GetUserID(c)

	var req updateIssueCollabNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.UpdateNote(issueID, noteID, userID, service.UpdateIssueCollabNoteRequest{Body: req.Body})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) DeleteNote(c *gin.Context) {
	if !h.requireJWT(c) {
		return
	}
	issueID := c.Param("iid")
	noteID := c.Param("nid")
	userID := middleware.GetUserID(c)

	if err := h.collabService.DeleteNote(issueID, noteID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *IssueCollabHandler) CreateQuestions(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req createIssueCollabQuestionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.CreateQuestions(issueID, userID, authorKind, service.CreateIssueCollabQuestionsRequest{Items: req.Items})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) AnswerQuestion(c *gin.Context) {
	if !h.requireJWT(c) {
		return
	}
	issueID := c.Param("iid")
	questionID := c.Param("qid")
	userID := middleware.GetUserID(c)
	authorKind := service.CollabAuthorKindFromAuth(middleware.IsJWTAuth(c))

	var req answerIssueCollabQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.collabService.AnswerQuestion(issueID, questionID, userID, authorKind, service.AnswerIssueCollabQuestionRequest{Answer: req.Answer})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *IssueCollabHandler) DeleteQuestion(c *gin.Context) {
	issueID := c.Param("iid")
	questionID := c.Param("qid")
	userID := middleware.GetUserID(c)

	if err := h.collabService.DeleteQuestion(issueID, questionID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *IssueCollabHandler) UpsertSummary(c *gin.Context) {
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

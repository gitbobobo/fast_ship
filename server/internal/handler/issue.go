package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssueHandler struct {
	issueService *service.IssueService
}

type updateIssueInternalMetaRequest struct {
	WorkflowStatus *model.IssueWorkflowStatus `json:"workflow_status"`
}

func NewIssueHandler(issueService *service.IssueService) *IssueHandler {
	return &IssueHandler{issueService: issueService}
}

func (h *IssueHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	filters := service.IssueListFilters{
		State:     c.Query("state"),
		Query:     c.Query("q"),
		Label:     c.Query("label"),
		Assignee:  c.Query("assignee"),
		Milestone: c.Query("milestone"),
		Workflow:  c.Query("workflow_status"),
		Sort:      c.DefaultQuery("sort", "updated_desc"),
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	items, total, err := h.issueService.List(projectID, userID, filters, page, pageSize)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, items, total, page, pageSize)
}

func (h *IssueHandler) Get(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	item, err := h.issueService.Get(issueID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, item)
}

func (h *IssueHandler) FilterOptions(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.issueService.GetFilterOptions(projectID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) ListComments(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}

	items, total, err := h.issueService.ListComments(issueID, userID, page, pageSize)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, items, total, page, pageSize)
}

func (h *IssueHandler) ListTimeline(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}

	items, total, err := h.issueService.ListTimeline(issueID, userID, page, pageSize)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, items, total, page, pageSize)
}

func (h *IssueHandler) Sync(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.issueService.SyncProjectIssues(projectID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) UpdateInternalMeta(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req updateIssueInternalMetaRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.WorkflowStatus == nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.UpdateInternalMeta(issueID, userID, *req.WorkflowStatus)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

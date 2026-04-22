package handler

import (
	"path/filepath"
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

type createInternalIssueRequest struct {
	Title          string                    `json:"title"`
	Body           string                    `json:"body"`
	WorkflowStatus model.IssueWorkflowStatus `json:"workflow_status"`
}

type updateInternalIssueRequest struct {
	Title       *string           `json:"title"`
	Body        *string           `json:"body"`
	State       *model.IssueState `json:"state"`
	StateReason *string           `json:"state_reason"`
}

type createInternalIssueCommentRequest struct {
	Body string `json:"body"`
}

type updateIssueInternalMetaRequest struct {
	WorkflowStatus *model.IssueWorkflowStatus `json:"workflow_status"`
}

type replaceIssueChecklistRequest struct {
	Items []service.IssueChecklistItemInput `json:"items"`
}

func NewIssueHandler(issueService *service.IssueService) *IssueHandler {
	return &IssueHandler{issueService: issueService}
}

func (h *IssueHandler) Create(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req createInternalIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.CreateInternalIssue(projectID, userID, service.CreateInternalIssueRequest{
		Title:          req.Title,
		Body:           req.Body,
		WorkflowStatus: req.WorkflowStatus,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) Update(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req updateInternalIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.UpdateInternalIssue(issueID, userID, service.UpdateInternalIssueRequest{
		Title:       req.Title,
		Body:        req.Body,
		State:       req.State,
		StateReason: req.StateReason,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	filters := service.IssueListFilters{
		State:     c.Query("state"),
		Query:     c.Query("q"),
		Label:     c.Query("label"),
		Source:    c.Query("source"),
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

func (h *IssueHandler) CreateComment(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req createInternalIssueCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.CreateInternalComment(issueID, userID, service.CreateInternalIssueCommentRequest{
		Body: req.Body,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
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

func (h *IssueHandler) ReplaceChecklist(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req replaceIssueChecklistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.ReplaceChecklist(issueID, userID, service.ReplaceIssueChecklistRequest{
		Items: req.Items,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) UploadAsset(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	fileName := header.Filename
	if filepath.Base(fileName) == "." || filepath.Base(fileName) == string(filepath.Separator) {
		fileName = ""
	}

	result, err := h.issueService.UploadInternalIssueAsset(issueID, userID, fileName, header.Size, file)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) UploadDraftAsset(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	fileName := header.Filename
	if filepath.Base(fileName) == "." || filepath.Base(fileName) == string(filepath.Separator) {
		fileName = ""
	}

	result, err := h.issueService.UploadDraftInternalIssueAsset(projectID, userID, fileName, header.Size, file)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) AssetContent(c *gin.Context) {
	assetID := c.Param("aid")
	userID := middleware.GetUserID(c)

	reader, mimeType, fileSize, err := h.issueService.GetIssueAssetContent(assetID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.DataFromReader(200, fileSize, mimeType, reader, nil)
}

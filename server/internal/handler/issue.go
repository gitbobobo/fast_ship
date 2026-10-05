package handler

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssueHandler struct {
	issueService    *service.IssueService
	shipHookService *service.IssueShipHookService
	collabService   *service.IssueCollabService
}

func NewIssueHandler(issueService *service.IssueService, shipHookService *service.IssueShipHookService, collabService *service.IssueCollabService) *IssueHandler {
	return &IssueHandler{issueService: issueService, shipHookService: shipHookService, collabService: collabService}
}

func (h *IssueHandler) Create(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req service.CreateInternalIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	var result *service.IssueResponse
	var err error
	if api.Deref(req.Source) == model.IssueSourceGitHub {
		if !middleware.IsJWTAuth(c) {
			middleware.HandleAppError(c, errs.ErrApiKeyForbidden)
			return
		}
		result, err = h.issueService.CreateGitHubIssue(projectID, userID, req)
	} else {
		result, err = h.issueService.CreateInternalIssue(projectID, userID, req)
	}
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) Update(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req service.UpdateInternalIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	if !middleware.IsJWTAuth(c) && (req.State != nil || req.StateReason != nil) {
		middleware.HandleAppError(c, errs.ErrApiKeyForbidden)
		return
	}

	result, err := h.issueService.UpdateInternalIssue(issueID, userID, req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	filters := parseIssueListFilters(c)
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

func parseIssueListFilters(c *gin.Context) service.IssueListFilters {
	return service.IssueListFilters{
		State:     c.Query("state"),
		Query:     c.Query("q"),
		Label:     c.Query("label"),
		Source:    c.Query("source"),
		Assignee:  c.Query("assignee"),
		Milestone: c.Query("milestone"),
		Workflow:  c.Query("workflow_status"),
		Sort:      c.DefaultQuery("sort", "updated_desc"),
	}
}

func (h *IssueHandler) Count(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	filters := parseIssueListFilters(c)

	count, err := h.issueService.CountIssues(projectID, userID, filters)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, api.CountIssues200JSONResponseBody_Data{Count: count})
}

func (h *IssueHandler) BatchCloseDone(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req api.BatchCloseDoneIssuesJSONBody
	_ = c.ShouldBindJSON(&req)
	if req.Source != nil && *req.Source != model.IssueSourceInternal && *req.Source != model.IssueSourceGitHub {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.BatchCloseDoneIssues(projectID, userID, string(api.Deref(req.Source)))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) Get(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	item, err := h.issueService.Get(issueID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	collab, err := h.collabService.GetArea(issueID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	item.Collab = collab

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

func (h *IssueHandler) RepoLabels(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.issueService.GetRepositoryLabels(projectID, userID)
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

	var req service.CreateInternalIssueCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.CreateInternalComment(issueID, userID, req, middleware.ActorLabel(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// MarkRead 记录当前用户已读到一个 Issue 的最新评论，仅 JWT 可调用。
func (h *IssueHandler) MarkRead(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	if err := h.issueService.MarkIssueRead(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
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

	var req api.UpdateIssueInternalMetaJSONBody
	if err := c.ShouldBindJSON(&req); err != nil || req.WorkflowStatus == nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.UpdateInternalMeta(issueID, userID, *req.WorkflowStatus, middleware.ActorLabel(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// BatchUpdateInternalMeta 批量更新 Issue 的 workflow_status，全局路径，item 以 issue_id 定位。
func (h *IssueHandler) BatchUpdateInternalMeta(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req api.BatchUpdateIssueInternalMetaJSONBody
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}
	for _, item := range req.Items {
		if strings.TrimSpace(item.IssueId) == "" {
			middleware.HandleAppError(c, errs.ErrInvalidParams)
			return
		}
	}

	result, err := h.issueService.BatchUpdateInternalMeta(userID, req.Items, middleware.ActorLabel(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) ReplaceChecklist(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req service.ReplaceIssueChecklistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.ReplaceChecklist(issueID, userID, req, middleware.ActorLabel(c))
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

	result, err := h.issueService.UploadInternalIssueAsset(issueID, userID, fileName, header.Size, file, middleware.ActorLabel(c))
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

func (h *IssueHandler) UpsertShipHook(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req service.UpsertShipHookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.shipHookService.UpsertShipHook(issueID, userID, req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueHandler) DeleteShipHook(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	if err := h.shipHookService.DeleteShipHook(issueID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
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

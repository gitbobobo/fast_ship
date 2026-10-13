package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// parseOptionalCursorQuery 解析可选的非负整数游标；空串返回 nil。
func parseOptionalCursorQuery(raw string) (*int, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return nil, err
	}
	return &parsed, nil
}

// T3EnvironmentHandler T3 环境连接与 T3 客户端端点（仅 JWT；INT-82/INT-85）。
type T3EnvironmentHandler struct {
	envService     *service.T3EnvironmentService
	threadsService *service.T3ThreadsService
}

func NewT3EnvironmentHandler(envService *service.T3EnvironmentService, threadsService *service.T3ThreadsService) *T3EnvironmentHandler {
	return &T3EnvironmentHandler{envService: envService, threadsService: threadsService}
}

// List GET /api/environments
func (h *T3EnvironmentHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	items, err := h.envService.List(c.Request.Context(), userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// Create POST /api/environments
func (h *T3EnvironmentHandler) Create(c *gin.Context) {
	var req service.CreateT3EnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	env, err := h.envService.Create(c.Request.Context(), userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, env)
}

// Update PUT /api/environments/:id
func (h *T3EnvironmentHandler) Update(c *gin.Context) {
	var req service.UpdateT3EnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	env, err := h.envService.Update(c.Request.Context(), userID, c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, env)
}

// Delete DELETE /api/environments/:id
func (h *T3EnvironmentHandler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.envService.Delete(c.Request.Context(), userID, c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ListThreads GET /api/threads
func (h *T3EnvironmentHandler) ListThreads(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.List(c.Request.Context(), userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetThread GET /api/environments/:id/threads/:threadId
func (h *T3EnvironmentHandler) GetThread(c *gin.Context) {
	userID := middleware.GetUserID(c)
	detail, err := h.threadsService.Detail(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, detail)
}

// GetThreadItem GET /api/environments/:id/threads/:threadId/items/:itemId
func (h *T3EnvironmentHandler) GetThreadItem(c *gin.Context) {
	userID := middleware.GetUserID(c)
	item, err := h.threadsService.ItemDetail(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("itemId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, item)
}

// GetComposerOptions GET /api/environments/:id/composer-options
func (h *T3EnvironmentHandler) GetComposerOptions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	options, err := h.threadsService.ComposerOptions(c.Request.Context(), userID, c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, options)
}

// GetWorkspaceOptions GET /api/environments/:id/projects/:projectId/workspace-options
func (h *T3EnvironmentHandler) GetWorkspaceOptions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	cursor, err := parseOptionalCursorQuery(c.Query("cursor"))
	if err != nil {
		response.BadRequest(c, 40001, "cursor 必须是非负整数")
		return
	}
	options, err := h.threadsService.WorkspaceOptions(c.Request.Context(), userID, c.Param("id"), c.Param("projectId"), c.Query("query"), cursor)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, options)
}

// CreateThread POST /api/environments/:id/threads
func (h *T3EnvironmentHandler) CreateThread(c *gin.Context) {
	var req service.T3CreateThreadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.CreateThread(c.Request.Context(), userID, c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// SendMessage POST /api/environments/:id/threads/:threadId/messages
func (h *T3EnvironmentHandler) SendMessage(c *gin.Context) {
	var req service.T3SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.SendMessage(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// StopThread POST /api/environments/:id/threads/:threadId/stop
func (h *T3EnvironmentHandler) StopThread(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.StopThread(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ResumeQueue POST /api/environments/:id/threads/:threadId/queue/resume
func (h *T3EnvironmentHandler) ResumeQueue(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.ResumeQueue(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// SteerQueuedRun POST /api/environments/:id/threads/:threadId/queue/:runId/steer
// 请求体无语义字段（target_run_id 由服务端解析为当前活动 run），容忍空 body。
func (h *T3EnvironmentHandler) SteerQueuedRun(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.SteerQueuedRun(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("runId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// CancelQueuedRun POST /api/environments/:id/threads/:threadId/queue/:runId/cancel
func (h *T3EnvironmentHandler) CancelQueuedRun(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.CancelQueuedRun(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("runId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// EditQueuedRun PUT /api/environments/:id/threads/:threadId/queue/:runId
func (h *T3EnvironmentHandler) EditQueuedRun(c *gin.Context) {
	// attachments 三态：字段缺省=保留原附件，[]=清空，非空=整组替换。
	// 生成类型把可选数组建成为 []T，区分不了「缺省」与「空」，这里旁路捕获。
	var raw struct {
		Text        *string         `json:"text"`
		Attachments json.RawMessage `json:"attachments"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	text := ""
	if raw.Text != nil {
		text = *raw.Text
	}
	var attachments *[]service.T3ChatAttachment
	if len(raw.Attachments) > 0 && string(raw.Attachments) != "null" {
		parsed := []service.T3ChatAttachment{}
		if err := json.Unmarshal(raw.Attachments, &parsed); err != nil {
			response.BadRequest(c, 40001, "attachments 无效: "+err.Error())
			return
		}
		attachments = &parsed
	}
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.EditQueuedRun(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("runId"), text, attachments)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ReorderQueuedRun POST /api/environments/:id/threads/:threadId/queue/:runId/reorder
func (h *T3EnvironmentHandler) ReorderQueuedRun(c *gin.Context) {
	var req service.T3ReorderQueuedRunRequst
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.ReorderQueuedRun(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("runId"), req.BeforeRunId)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// RespondToRequest POST /api/environments/:id/threads/:threadId/requests/:requestId/respond
func (h *T3EnvironmentHandler) RespondToRequest(c *gin.Context) {
	var req service.T3RespondRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.RespondToRequest(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("requestId"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// DismissRequest POST /api/environments/:id/threads/:threadId/requests/:requestId/dismiss
func (h *T3EnvironmentHandler) DismissRequest(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.DismissRequest(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("requestId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// AnswerSecretRequest POST /api/environments/:id/threads/:threadId/items/:itemId/secret-answer
func (h *T3EnvironmentHandler) AnswerSecretRequest(c *gin.Context) {
	var req service.T3AnswerSecretRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	if err := h.threadsService.AnswerSecret(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("itemId"), &req); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// UploadAttachment POST /api/environments/:id/attachments
func (h *T3EnvironmentHandler) UploadAttachment(c *gin.Context) {
	userID := middleware.GetUserID(c)
	// 原始字节流转发：不经 ShouldBind/multipart（避免落临时文件），
	// 也不做 base64。Content-Type 即图片 MIME，X-File-Name 是显示名。
	attachment, err := h.threadsService.CreateAttachmentUpload(
		c.Request.Context(), userID, c.Param("id"),
		c.GetHeader("X-File-Name"), c.ContentType(),
		c.Request.ContentLength, c.Request.Body,
	)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, attachment)
}

// GetAttachmentContent GET /api/environments/:id/attachments/:attachmentId/content
func (h *T3EnvironmentHandler) GetAttachmentContent(c *gin.Context) {
	userID := middleware.GetUserID(c)
	contentType, data, err := h.threadsService.GetAttachmentContent(c.Request.Context(), userID, c.Param("id"), c.Param("attachmentId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	c.Data(http.StatusOK, contentType, data)
}

// DeleteAttachment DELETE /api/environments/:id/attachments/:attachmentId
func (h *T3EnvironmentHandler) DeleteAttachment(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.threadsService.DeleteAttachment(c.Request.Context(), userID, c.Param("id"), c.Param("attachmentId")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

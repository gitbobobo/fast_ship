package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// t3StreamHeartbeat 是线程事件流的心跳周期，与 bot SSE 一致。
const t3StreamHeartbeat = 15 * time.Second

// parseOptionalCursorQuery 解析可选的非负整数游标；空串返回 nil。
// queuedEditText 排队编辑的 text 必填：缺省会把已有正文清成空串。
// 显式 "" 仍放行（只改图片，或清掉占位正文）。
func queuedEditText(raw *string) (string, bool) {
	if raw == nil {
		return "", false
	}
	return *raw, true
}

func parseOptionalCursorQuery(raw string) (*int, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return nil, err
	}
	if parsed < 0 {
		return nil, strconv.ErrRange
	}
	return &parsed, nil
}

// denyReadOnly 写入口在解码 body 之前先查会话能力：只读环境统一 40300，
// 不因非法 JSON 先落到 40001。环境不存在仍是 40417。
func (h *T3EnvironmentHandler) denyReadOnly(c *gin.Context) bool {
	if err := h.threadsService.RequireOperate(c.Request.Context(), middleware.GetUserID(c), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return true
	}
	return false
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

// StreamThreadEvents GET /api/threads/stream（INT-132）：shell 变更推送。
//
// 流程与 bot SSE（bot_stream.go）一致：先注册订阅再写流，请求期间不重新校验
// JWT（长连接与 bot 流同策略，不做额外 DB 检查）。首帧是无 environment_id 的
// 全量刷新信号；之后逐条转发 hub 发布的合并事件（Drain 摘取）。只有当前
// goroutine 写 ResponseWriter；每次写前设写超时（writer 不支持就跳过），
// 写失败立即退出——断开的客户端不能拖住 handler。
func (h *T3EnvironmentHandler) StreamThreadEvents(c *gin.Context) {
	userID := middleware.GetUserID(c)
	// 先注册订阅：注册与首帧之间产生的发布由订阅缓冲兜住，不会丢。
	sub := h.threadsService.SubscribeShellEvents(userID)
	defer sub.Close()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	controller := http.NewResponseController(c.Writer)
	writeFrame := func(frame string) bool {
		// 写超时防止「客户端保持连接但停止读取」把 handler 与订阅永久挂住。
		// 生产环境的 RequestLog 包装已透传 Unwrap，deadline 才能落到真实连接上。
		// 不支持的 writer（如测试里的 recorder）只能靠请求 ctx 兜底，跳过即可。
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(c.Writer, frame); err != nil {
			return false
		}
		if err := controller.Flush(); err != nil {
			return false
		}
		return true
	}

	// 首帧：全局刷新信号（无 environment_id），浏览器据此拉一次全量列表。
	if !writeFrame("event: shell\ndata: {\"full\":true}\n\n") {
		return
	}

	heartbeat := time.NewTicker(t3StreamHeartbeat)
	defer heartbeat.Stop()
	requestCtx := c.Request.Context()

	for {
		select {
		case <-requestCtx.Done():
			return
		case <-heartbeat.C:
			// SSE 注释心跳：穿透反向代理的空闲超时，不算事件。
			if !writeFrame(": ping\n\n") {
				return
			}
		case <-sub.C():
			events := sub.Drain()
			if events == nil {
				// 订阅已关闭（hub 停机）：nil 区别于「无事件」的空切片。
				return
			}
			for i := range events {
				payload, err := json.Marshal(events[i])
				if err != nil {
					continue // 纯数据结构，编码失败只可能来自内部错误，跳过该条
				}
				if !writeFrame("event: shell\ndata: " + string(payload) + "\n\n") {
					return
				}
			}
		}
	}
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
	if h.denyReadOnly(c) {
		return
	}
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
	if h.denyReadOnly(c) {
		return
	}
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
	if h.denyReadOnly(c) {
		return
	}
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
	text, ok := queuedEditText(raw.Text)
	if !ok {
		response.BadRequest(c, 40001, "text 不能缺省")
		return
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
	if h.denyReadOnly(c) {
		return
	}
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
	if h.denyReadOnly(c) {
		return
	}
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
	if h.denyReadOnly(c) {
		return
	}
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
	if h.denyReadOnly(c) {
		return
	}
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

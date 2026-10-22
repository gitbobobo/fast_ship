package handler

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// BotHandler 机器人管理面（INT-101）：全部仅 JWT——管理、消息、结果、确认与
// SSE 都按机器人归属取用户身份，不把机器人降格为 API Key 身份，也不接受
// 请求体指定 user_id。

type BotHandler struct {
	bots *service.BotService
}

func NewBotHandler(bots *service.BotService) *BotHandler {
	return &BotHandler{bots: bots}
}

func (h *BotHandler) List(c *gin.Context) {
	items, err := h.bots.List(middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *BotHandler) Create(c *gin.Context) {
	var req service.CreateBotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.bots.Create(middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) TestConnection(c *gin.Context) {
	var req service.TestConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.bots.TestConnection(middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) Get(c *gin.Context) {
	result, err := h.bots.Get(middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) Update(c *gin.Context) {
	var req service.UpdateBotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.bots.Update(middleware.GetUserID(c), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) Delete(c *gin.Context) {
	if err := h.bots.Delete(middleware.GetUserID(c), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ---------- 消息 ----------

// GetChatSnapshot 聊天页初始读取：同一读取点的消息/调用/run 与事件水位。
func (h *BotHandler) GetChatSnapshot(c *gin.Context) {
	limit := 50
	if raw := c.Query("message_limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			response.BadRequest(c, 40001, "message_limit 必须是 1-200 的整数")
			return
		}
		limit = parsed
	}
	snap, err := h.bots.GetChatSnapshot(middleware.GetUserID(c), c.Param("id"), limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, snap)
}

func (h *BotHandler) ListMessages(c *gin.Context) {
	before := parseSeqParam(c, "before_seq")
	after := parseSeqParam(c, "after_seq")
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			response.BadRequest(c, 40001, "limit 必须是 1-200 的整数")
			return
		}
		limit = parsed
	}
	items, total, err := h.bots.ListMessages(middleware.GetUserID(c), c.Param("id"), before, after, limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total})
}

func (h *BotHandler) SendMessage(c *gin.Context) {
	var req service.SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.bots.SendMessage(middleware.GetUserID(c), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ---------- run ----------

func (h *BotHandler) ListRuns(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	items, err := h.bots.ListRuns(middleware.GetUserID(c), c.Param("id"), limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *BotHandler) GetRun(c *gin.Context) {
	result, err := h.bots.GetRun(middleware.GetUserID(c), c.Param("id"), c.Param("runId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) StopRun(c *gin.Context) {
	result, err := h.bots.RequestStop(middleware.GetUserID(c), c.Param("id"), c.Param("runId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ---------- 调用 ----------

func (h *BotHandler) ListCalls(c *gin.Context) {
	var statuses []string
	if raw := c.Query("status"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				statuses = append(statuses, part)
			}
		}
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			response.BadRequest(c, 40001, "limit 必须是 1-200 的整数")
			return
		}
		limit = parsed
	}
	items, err := h.bots.ListCalls(middleware.GetUserID(c), c.Param("id"), statuses, limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *BotHandler) GetCall(c *gin.Context) {
	result, err := h.bots.GetCall(middleware.GetUserID(c), c.Param("id"), c.Param("callId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) GetCallResult(c *gin.Context) {
	result, err := h.bots.GetCallResult(middleware.GetUserID(c), c.Param("id"), c.Param("callId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) CallDecision(c *gin.Context) {
	var req service.CallDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.bots.DecideCall(middleware.GetUserID(c), c.Param("id"), c.Param("callId"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) CancelCall(c *gin.Context) {
	result, err := h.bots.CancelCall(middleware.GetUserID(c), c.Param("id"), c.Param("callId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ---------- 定时任务（INT-103：用户只能查看与开关，没有创建/编辑端点） ----------

func (h *BotHandler) ListScheduleTasks(c *gin.Context) {
	items, err := h.bots.ListScheduleTasks(middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *BotHandler) GetScheduleTask(c *gin.Context) {
	result, err := h.bots.GetScheduleTask(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *BotHandler) ListScheduleTaskRuns(c *gin.Context) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := c.Query("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			response.BadRequest(c, 40001, "offset 必须是非负整数")
			return
		}
		offset = parsed
	}
	items, err := h.bots.ListScheduleTaskRuns(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"), limit, offset)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// SetScheduleTaskEnabled body 只有 {"enabled":bool}；未知字段与缺省 enabled
// 都按 400 拒绝（用户对任务唯一的写操作）。
func (h *BotHandler) SetScheduleTaskEnabled(c *gin.Context) {
	var req service.SetScheduleTaskEnabledRequest
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: body 只接受 {\"enabled\":bool}")
		return
	}
	if req.Enabled == nil {
		response.BadRequest(c, 40001, "请求参数无效: enabled 必填")
		return
	}
	result, err := h.bots.SetScheduleTaskEnabled(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"), *req.Enabled)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ClearScheduleTaskState 清空定时任务的小状态（INT-123）：幂等，返回清空后
// 的任务投影。
func (h *BotHandler) ClearScheduleTaskState(c *gin.Context) {
	result, err := h.bots.ClearScheduleTaskState(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// StopScheduleRun 停止一条定时任务运行（INT-137）：置 stop_requested 并取消
// 执行槽，run 在安全点收尾 stopped，不追加主线事件。
func (h *BotHandler) StopScheduleRun(c *gin.Context) {
	result, err := h.bots.StopScheduleRun(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"), c.Param("runId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// ---------- 授权 ----------

func (h *BotHandler) ListAuthorizations(c *gin.Context) {
	activeOnly := false
	if raw := c.Query("active"); raw == "true" || raw == "1" {
		activeOnly = true
	}
	items, err := h.bots.ListAuthorizations(middleware.GetUserID(c), c.Param("id"), activeOnly)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *BotHandler) RevokeAuthorization(c *gin.Context) {
	if err := h.bots.RevokeAuthorization(middleware.GetUserID(c), c.Param("id"), c.Param("authorizationId")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

func parseSeqParam(c *gin.Context, name string) int64 {
	raw := c.Query(name)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

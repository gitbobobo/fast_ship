package handler

import (
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

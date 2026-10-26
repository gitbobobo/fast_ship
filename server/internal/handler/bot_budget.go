package handler

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// 机器人与定时任务的周期预算（INT-164）：JWT 用户侧读写。预算只影响告警，
// 不存在时不改变任何运行行为；周期段只接受 daily / monthly（路径段非法即
// 40001），金额与阈值校验都在服务层。

// ListBotBudgets 机器人预算列表（每日/每月各至多一条）与当前周期花费。
func (h *BotHandler) ListBotBudgets(c *gin.Context) {
	items, err := h.bots.ListBotBudgets(middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// UpsertBotBudget 覆盖机器人某周期的预算（body: amount_usd, thresholds）。
func (h *BotHandler) UpsertBotBudget(c *gin.Context) {
	var req service.BotBudgetUpsertRequest
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: body 只接受 {\"amount_usd\":number,\"thresholds\":[number]}")
		return
	}
	result, err := h.bots.UpsertBotBudget(middleware.GetUserID(c), c.Param("id"), c.Param("period"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// DeleteBotBudget 删除机器人某周期的预算（幂等）。
func (h *BotHandler) DeleteBotBudget(c *gin.Context) {
	if err := h.bots.DeleteBotBudget(middleware.GetUserID(c), c.Param("id"), c.Param("period")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ListScheduleTaskBudgets 定时任务预算列表与当前周期花费。
func (h *BotHandler) ListScheduleTaskBudgets(c *gin.Context) {
	items, err := h.bots.ListScheduleTaskBudgets(middleware.GetUserID(c), c.Param("id"), c.Param("taskId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// UpsertScheduleTaskBudget 覆盖定时任务某周期的预算。
func (h *BotHandler) UpsertScheduleTaskBudget(c *gin.Context) {
	var req service.BotBudgetUpsertRequest
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: body 只接受 {\"amount_usd\":number,\"thresholds\":[number]}")
		return
	}
	result, err := h.bots.UpsertScheduleTaskBudget(middleware.GetUserID(c), c.Param("id"),
		c.Param("taskId"), c.Param("period"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// DeleteScheduleTaskBudget 删除定时任务某周期的预算（幂等）。
func (h *BotHandler) DeleteScheduleTaskBudget(c *gin.Context) {
	if err := h.bots.DeleteScheduleTaskBudget(middleware.GetUserID(c), c.Param("id"),
		c.Param("taskId"), c.Param("period")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

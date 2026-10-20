package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// ReviewHandler PR 审查的 JWT 管理面（INT-135 1b）：审查总览、项目审查设置
// 读写与审查记录查询。review-agent 运行令牌端点在 1c 接入，不经本 handler。
type ReviewHandler struct {
	reviewService *service.ReviewService
}

func NewReviewHandler(reviewService *service.ReviewService) *ReviewHandler {
	return &ReviewHandler{reviewService: reviewService}
}

// ListProjects GET /api/reviews
func (h *ReviewHandler) ListProjects(c *gin.Context) {
	items, err := h.reviewService.ListReviewProjects(c.Request.Context(), middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetSettings GET /api/projects/:id/review-settings
func (h *ReviewHandler) GetSettings(c *gin.Context) {
	settings, err := h.reviewService.GetSettings(c.Request.Context(), middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateSettings PUT /api/projects/:id/review-settings
func (h *ReviewHandler) UpdateSettings(c *gin.Context) {
	var req service.ReviewSettingsInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	settings, err := h.reviewService.UpdateSettings(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, settings)
}

// Rerun POST /api/projects/:id/reviews/rerun
func (h *ReviewHandler) Rerun(c *gin.Context) {
	var req struct {
		PRNumber     int    `json:"pr_number"`
		RepoFullName string `json:"repo_full_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PRNumber < 1 {
		response.BadRequest(c, 40001, "pr_number 必须是 >=1 的整数")
		return
	}
	out, err := h.reviewService.RerunReview(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), req.PRNumber, req.RepoFullName)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": out.Accepted, "head_sha": out.HeadSHA, "round_number": out.RoundNumber})
}

// ListRecords GET /api/projects/:id/review-records?limit=
func (h *ReviewHandler) ListRecords(c *gin.Context) {
	limit := 0 // 0 由 service 取默认 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			response.BadRequest(c, 40001, "limit 必须是 1-100 的整数")
			return
		}
		limit = parsed
	}
	items, err := h.reviewService.ListReviewRecords(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), limit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

package handler

import (
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// ReviewAgentHandler /api/review-agent 运行令牌端点（INT-135 1c）：
// 鉴权由 middleware.RequireRunToken 完成，这里只取 run_id 调 service。
type ReviewAgentHandler struct {
	reviewService *service.ReviewService
}

func NewReviewAgentHandler(reviewService *service.ReviewService) *ReviewAgentHandler {
	return &ReviewAgentHandler{reviewService: reviewService}
}

// PullRequest GET /api/review-agent/pull-request
func (h *ReviewAgentHandler) PullRequest(c *gin.Context) {
	out, err := h.reviewService.AgentPullRequest(c.Request.Context(), middleware.GetRunID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Diff GET /api/review-agent/pull-request/diff
func (h *ReviewAgentHandler) Diff(c *gin.Context) {
	out, err := h.reviewService.AgentDiff(c.Request.Context(), middleware.GetRunID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// History GET /api/review-agent/history
func (h *ReviewAgentHandler) History(c *gin.Context) {
	content, err := h.reviewService.AgentHistory(c.Request.Context(), middleware.GetRunID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"content": content})
}

// Screenshots GET /api/review-agent/screenshots
func (h *ReviewAgentHandler) Screenshots(c *gin.Context) {
	items, changed, err := h.reviewService.AgentScreenshots(c.Request.Context(), middleware.GetRunID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "changed": changed})
}

// ScreenshotImage GET /api/review-agent/screenshots/:assetId
func (h *ReviewAgentHandler) ScreenshotImage(c *gin.Context) {
	path, mime, err := h.reviewService.AgentScreenshotImage(c.Request.Context(), middleware.GetRunID(c), c.Param("assetId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer f.Close()
	c.Header("Content-Type", mime)
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
}

// reviewResultMaxBodyBytes /result 请求体上限：结构化结论体量有限，无上限
// 的 ReadAll 会让超大 body 撑爆内存；超限按结果非法（40004）返回。
const reviewResultMaxBodyBytes = 2 << 20

// SubmitResult POST /api/review-agent/result
func (h *ReviewAgentHandler) SubmitResult(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, reviewResultMaxBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			response.BadRequest(c, errs.ErrReviewResultInvalid.Code, "请求体超过 2MB 上限")
			return
		}
		response.BadRequest(c, 40001, "读取请求体失败")
		return
	}
	if err := h.reviewService.AgentSubmitResult(c.Request.Context(), middleware.GetRunID(c), body); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

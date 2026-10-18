package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// QuotaHandler 订阅套餐额度（INT-109）。管理端点仅 JWT；
// GET /accounts 经 RequireAuth 对 JWT 与 API Key 均开放（只读）。
type QuotaHandler struct {
	quotaService *service.QuotaService
}

func NewQuotaHandler(quotaService *service.QuotaService) *QuotaHandler {
	return &QuotaHandler{quotaService: quotaService}
}

func (h *QuotaHandler) ListProviders(c *gin.Context) {
	providers, err := h.quotaService.ListProviders()
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, providers)
}

func (h *QuotaHandler) ListAccounts(c *gin.Context) {
	userID := middleware.GetUserID(c)
	accounts, err := h.quotaService.List(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, accounts)
}

func (h *QuotaHandler) CreateAccount(c *gin.Context) {
	var req service.CreateQuotaAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.quotaService.Create(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *QuotaHandler) UpdateAccount(c *gin.Context) {
	id := c.Param("id")
	var req service.UpdateQuotaAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.quotaService.Update(userID, id, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *QuotaHandler) DeleteAccount(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.quotaService.Delete(userID, id); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// RefreshAccount 立即抓一次。42900 映射 HTTP 429（HandleAppError 的区间表不覆盖
// 429xx，会落到 400；这里显式分支保证状态码与业务码语义一致）。
func (h *QuotaHandler) RefreshAccount(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.quotaService.Refresh(userID, id)
	if err != nil {
		if appErr, ok := err.(*errs.AppError); ok && appErr.Code == errs.ErrQuotaRefreshTooOften.Code {
			response.Error(c, http.StatusTooManyRequests, appErr.Code, appErr.Message)
			return
		}
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *QuotaHandler) Test(c *gin.Context) {
	var req service.TestQuotaCredentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	result, err := h.quotaService.Probe(&req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// StartDeviceAuthorization 发起浏览器设备授权。上游不可用（50204）按区间表
// 映射 HTTP 502（HandleAppError 覆盖 502xx）。
func (h *QuotaHandler) StartDeviceAuthorization(c *gin.Context) {
	var req service.StartQuotaDeviceAuthorizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.quotaService.StartDeviceAuthorization(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetDeviceAuthorization 轮询授权进度；会话不存在/过期 40431 → HTTP 404。
func (h *QuotaHandler) GetDeviceAuthorization(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.quotaService.GetDeviceAuthorization(userID, c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// CancelDeviceAuthorization 取消会话；DELETE 成功返回 data: null。
func (h *QuotaHandler) CancelDeviceAuthorization(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.quotaService.CancelDeviceAuthorization(userID, c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

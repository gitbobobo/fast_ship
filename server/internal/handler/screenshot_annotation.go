package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type ScreenshotAnnotationHandler struct {
	annotationService *service.ScreenshotAnnotationService
}

func NewScreenshotAnnotationHandler(annotationService *service.ScreenshotAnnotationService) *ScreenshotAnnotationHandler {
	return &ScreenshotAnnotationHandler{annotationService: annotationService}
}

// List 返回项目全部标注；status/issue_id/screen_id query 过滤，status 非法值 40001。
func (h *ScreenshotAnnotationHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.annotationService.List(projectID, userID, service.ScreenshotAnnotationListFilters{
		Status:   c.Query("status"),
		IssueID:  c.Query("issue_id"),
		ScreenID: c.Query("screen_id"),
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// Create 在版本上新建标注（路由层已限 JWT）；created_by 取请求者可读标识。
func (h *ScreenshotAnnotationHandler) Create(c *gin.Context) {
	versionID := c.Param("vid")
	userID := middleware.GetUserID(c)
	createdBy := middleware.ActorLabel(c)

	var req api.CreateScreenshotAnnotationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.annotationService.Create(versionID, userID, createdBy, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// Update 更新标注文字/状态/关联 Issue；API Key 与 JWT 的权限分工在 service 内按凭证类型判定。
func (h *ScreenshotAnnotationHandler) Update(c *gin.Context) {
	annotationID := c.Param("aid")
	userID := middleware.GetUserID(c)

	var req api.UpdateScreenshotAnnotationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.annotationService.Update(annotationID, userID, &req, !middleware.IsJWTAuth(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ScreenshotAnnotationHandler) Delete(c *gin.Context) {
	annotationID := c.Param("aid")
	userID := middleware.GetUserID(c)

	if err := h.annotationService.Delete(annotationID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
}

// Crop 输出标注框选区域的 PNG 裁剪图，响应头与版本 content 端点一致。
func (h *ScreenshotAnnotationHandler) Crop(c *gin.Context) {
	annotationID := c.Param("aid")
	userID := middleware.GetUserID(c)

	reader, err := h.annotationService.Crop(annotationID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=300")
	c.DataFromReader(http.StatusOK, int64(reader.Len()), "image/png", reader, nil)
}

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

// parseAnnotationFilters 解析标注列表 query：status/outdated/issue_id/screen_id
// 与逗号多值的维度过滤（__none__ = 未标注桶）。
func parseAnnotationFilters(c *gin.Context) (service.ScreenshotAnnotationListFilters, error) {
	filters := service.ScreenshotAnnotationListFilters{
		Status:   c.Query("status"),
		IssueID:  c.Query("issue_id"),
		ScreenID: c.Query("screen_id"),
	}
	outdated, err := parseBoolQuery(c.Query("outdated"))
	if err != nil {
		return filters, err
	}
	filters.Outdated = outdated
	// 维度过滤与截图列表共用同一套规范化，保证两个 GET 的过滤口径一致
	if filters.Platforms, filters.IncludeNoPlat, err = service.ParseScreenshotPlatformFilter(c.Query("platform")); err != nil {
		return filters, err
	}
	if filters.Locales, filters.IncludeNoLocale, err = service.ParseScreenshotLocaleFilter(c.Query("locale")); err != nil {
		return filters, err
	}
	filters.Themes, filters.IncludeNoTheme, err = service.ParseScreenshotThemeFilter(c.Query("theme"))
	return filters, err
}

// List 返回项目全部标注；query 过滤，非法值 40001。
func (h *ScreenshotAnnotationHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	filters, err := parseAnnotationFilters(c)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	result, err := h.annotationService.List(projectID, userID, filters)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// Batch 对过滤范围执行批量确认保留/标记已解决（路由层已限 JWT）。
func (h *ScreenshotAnnotationHandler) Batch(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req api.ScreenshotAnnotationBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.annotationService.Batch(projectID, userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// Create 在截图上新建标注（路由层已限 JWT）；created_by 取请求者可读标识。
func (h *ScreenshotAnnotationHandler) Create(c *gin.Context) {
	screenID := c.Param("sid")
	userID := middleware.GetUserID(c)
	createdBy := middleware.ActorLabel(c)

	var req api.CreateScreenshotAnnotationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.annotationService.Create(screenID, userID, createdBy, &req)
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

// Crop 输出标注框选区域的 PNG 裁剪图，响应头与截图 content 端点一致。
// HEAD 变体只跑存在性/权限/像素预算校验，不解码、不生成 PNG
func (h *ScreenshotAnnotationHandler) Crop(c *gin.Context) {
	annotationID := c.Param("aid")
	userID := middleware.GetUserID(c)

	if c.Request.Method == http.MethodHead {
		if err := h.annotationService.CheckCrop(annotationID, userID); err != nil {
			middleware.HandleAppError(c, err)
			return
		}
		c.Header("Content-Disposition", "inline")
		c.Header("Cache-Control", "private, max-age=300")
		c.Data(http.StatusOK, "image/png", nil)
		return
	}

	reader, release, err := h.annotationService.Crop(c.Request.Context(), annotationID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer release()

	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=300")
	c.DataFromReader(http.StatusOK, int64(reader.Len()), "image/png", reader, nil)
}

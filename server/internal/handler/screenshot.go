package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// multipart 头部与其他表单字段的体积余量，确保恰好等于 MaxFileSize 的文件能通过
const screenshotMultipartOverheadBytes = 1 << 20

type ScreenshotHandler struct {
	screenshotService *service.ScreenshotService
	maxUploadBytes    int64
}

func NewScreenshotHandler(screenshotService *service.ScreenshotService, cfg *config.Config) *ScreenshotHandler {
	return &ScreenshotHandler{
		screenshotService: screenshotService,
		maxUploadBytes:    cfg.Upload.MaxFileSize,
	}
}

func (h *ScreenshotHandler) Upload(c *gin.Context) {
	// 先限制整个请求体再碰表单：FormFile/GetPostForm 会把 multipart 全部读入内存或临时文件，
	// 只靠 service 层的 LimitReader 挡不住超限 body 落临时盘
	if h.maxUploadBytes > 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadBytes+screenshotMultipartOverheadBytes)
	}

	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	uploadedBy := middleware.GetUserName(c)
	if uploadedBy == "" {
		if middleware.IsJWTAuth(c) {
			uploadedBy = "Web 用户"
		} else {
			apiKeyName := middleware.GetAPIKeyName(c)
			if apiKeyName == "" {
				uploadedBy = "API Key"
			} else {
				uploadedBy = "API Key: " + apiKeyName
			}
		}
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	// 维度字段沿用表单存在性语义：字段出现才生效（空串清除为未标注），不出现保持原值
	var platform, locale, theme *string
	if v, ok := c.GetPostForm("platform"); ok {
		platform = &v
	}
	if v, ok := c.GetPostForm("locale"); ok {
		locale = &v
	}
	if v, ok := c.GetPostForm("theme"); ok {
		theme = &v
	}

	result, err := h.screenshotService.Upload(&service.ScreenshotUploadInput{
		ProjectID:  projectID,
		UserID:     userID,
		ScreenKey:  c.PostForm("screen_key"),
		Title:      c.PostForm("title"),
		Platform:   platform,
		Locale:     locale,
		Theme:      theme,
		FileName:   header.Filename,
		UploadedBy: uploadedBy,
		Reader:     file,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// parseScreenshotDimensionFilters 把 query 上的逗号多值维度过滤解析成 repo 过滤结构；
// 各维度按自己的规则规范化（platform 小写、locale BCP 47、theme light|dark），
// 非法值返回 40001。
func parseScreenshotDimensionFilters(c *gin.Context) (repository.ScreenshotScreenFilters, error) {
	var filters repository.ScreenshotScreenFilters
	var err error
	if filters.Platforms, filters.IncludeNoPlat, err = service.ParseScreenshotPlatformFilter(c.Query("platform")); err != nil {
		return filters, err
	}
	if filters.Locales, filters.IncludeNoLocale, err = service.ParseScreenshotLocaleFilter(c.Query("locale")); err != nil {
		return filters, err
	}
	filters.Themes, filters.IncludeNoTheme, err = service.ParseScreenshotThemeFilter(c.Query("theme"))
	return filters, err
}

func (h *ScreenshotHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	filters, err := parseScreenshotDimensionFilters(c)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	result, err := h.screenshotService.List(projectID, userID, filters)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ScreenshotHandler) Update(c *gin.Context) {
	screenID := c.Param("sid")
	userID := middleware.GetUserID(c)

	var req api.UpdateScreenshotScreenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.screenshotService.Update(screenID, userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ScreenshotHandler) DeleteScreen(c *gin.Context) {
	screenID := c.Param("sid")
	userID := middleware.GetUserID(c)

	if err := h.screenshotService.DeleteScreen(screenID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
}

func (h *ScreenshotHandler) Content(c *gin.Context) {
	screenID := c.Param("sid")
	userID := middleware.GetUserID(c)

	reader, mimeType, fileSize, err := h.screenshotService.GetScreenContent(screenID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	// 同一道同源内容不变量：这条出口认 ?token=，内容由用户上传（上传只按前 512
	// 字节嗅探求 image/*，「位图魔数 + HTML 体」的多态文件能过），不能以同源可
	// 执行文档交付。这里保留 inline（「在标签页里看截图」是产品行为），所以用
	// 图片文档那一档：nosniff + CSP 只给 sandbox，不给 default-src 'none'。
	setSameOriginInlineImageHardeningHeaders(c.Writer.Header())
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=300")
	c.DataFromReader(200, fileSize, mimeType, reader, nil)
}

// parseBoolQuery 解析布尔 query 参数；未提供返回 nil，非法值由调用方拒绝。
func parseBoolQuery(raw string) (*bool, error) {
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errs.ErrInvalidParams
	}
	return &v, nil
}

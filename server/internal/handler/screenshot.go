package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
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

	var group *string
	if value, ok := c.GetPostForm("group"); ok {
		group = &value
	}

	result, err := h.screenshotService.Upload(&service.ScreenshotUploadInput{
		ProjectID:  projectID,
		UserID:     userID,
		ScreenKey:  c.PostForm("screen_key"),
		Group:      group,
		Title:      c.PostForm("title"),
		Note:       c.PostForm("note"),
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

func (h *ScreenshotHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.screenshotService.List(projectID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ScreenshotHandler) Get(c *gin.Context) {
	screenID := c.Param("sid")
	userID := middleware.GetUserID(c)

	result, err := h.screenshotService.Get(screenID, userID)
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

func (h *ScreenshotHandler) DeleteVersion(c *gin.Context) {
	versionID := c.Param("vid")
	userID := middleware.GetUserID(c)

	if err := h.screenshotService.DeleteVersion(versionID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
}

func (h *ScreenshotHandler) Content(c *gin.Context) {
	versionID := c.Param("vid")
	userID := middleware.GetUserID(c)

	reader, mimeType, fileSize, err := h.screenshotService.GetVersionContent(versionID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=300")
	c.DataFromReader(200, fileSize, mimeType, reader, nil)
}

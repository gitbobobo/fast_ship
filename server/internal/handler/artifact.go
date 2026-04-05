package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type ArtifactHandler struct {
	artifactService *service.ArtifactService
}

func NewArtifactHandler(artifactService *service.ArtifactService) *ArtifactHandler {
	return &ArtifactHandler{artifactService: artifactService}
}

func (h *ArtifactHandler) Upload(c *gin.Context) {
	vid := c.Param("vid")
	userID := middleware.GetUserID(c)
	platform := c.PostForm("platform")

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	result, err := h.artifactService.Upload(vid, userID, header.Filename, header.Size, platform, file)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ArtifactHandler) Delete(c *gin.Context) {
	aid := c.Param("aid")
	userID := middleware.GetUserID(c)

	if err := h.artifactService.Delete(aid, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}

func (h *ArtifactHandler) Download(c *gin.Context) {
	aid := c.Param("aid")

	reader, fileName, err := h.artifactService.Download(aid)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	c.Header("Content-Disposition", "attachment; filename="+fileName)
	c.Header("Content-Type", "application/octet-stream")
	c.DataFromReader(200, -1, "application/octet-stream", reader, nil)
}

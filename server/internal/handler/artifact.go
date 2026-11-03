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
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	result, err := h.artifactService.Upload(vid, userID, header.Filename, header.Size, platform, uploadedBy, file)
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

	response.SuccessEmpty(c)
}

func (h *ArtifactHandler) Download(c *gin.Context) {
	aid := c.Param("aid")
	userID := middleware.GetUserID(c)

	reader, fileName, err := h.artifactService.Download(aid, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	// 下载档收口（INT-182）：原先的 disposition 是裸拼 filename——未消毒、未
	// 按 RFC 6266/5987，改走出口 attachment 档后一并修掉，硬化头与档位缓存
	// 同时从无到有补齐。
	egress.OctetDownload(c, fileName, -1, reader)
}

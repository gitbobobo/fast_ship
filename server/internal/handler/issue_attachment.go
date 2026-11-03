package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// multipart 头部与其他表单字段的体积余量，确保恰好等于 MaxFileSize 的文件能通过
const attachmentMultipartOverheadBytes = 1 << 20

type IssueAttachmentHandler struct {
	attachmentService *service.IssueAttachmentService
	maxUploadBytes    int64
}

func NewIssueAttachmentHandler(attachmentService *service.IssueAttachmentService, cfg *config.Config) *IssueAttachmentHandler {
	return &IssueAttachmentHandler{
		attachmentService: attachmentService,
		maxUploadBytes:    cfg.Upload.MaxFileSize,
	}
}

func (h *IssueAttachmentHandler) Upload(c *gin.Context) {
	// 先限制整个请求体再碰表单：FormFile 会把超限 multipart 读进临时文件，
	// 只靠 service 层的 LimitReader 挡不住超限 body 落临时盘
	if h.maxUploadBytes > 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadBytes+attachmentMultipartOverheadBytes)
	}

	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

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

	result, err := h.attachmentService.Upload(issueID, userID, sanitizeAttachmentFileName(header.Filename), file, middleware.ActorLabel(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *IssueAttachmentHandler) Delete(c *gin.Context) {
	aid := c.Param("aid")
	userID := middleware.GetUserID(c)

	if err := h.attachmentService.Delete(aid, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
}

func (h *IssueAttachmentHandler) Download(c *gin.Context) {
	aid := c.Param("aid")
	userID := middleware.GetUserID(c)

	reader, fileName, err := h.attachmentService.Download(aid, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	defer reader.Close()

	// 无缓存同源出口 attachment 档（INT-182）：原先只发 disposition、没有硬化
	// 头，是同源出口的漏网——附件同样认 ?token=，从无到有补齐硬化头与档位缓存。
	egress.OctetDownload(c, fileName, -1, reader)
}

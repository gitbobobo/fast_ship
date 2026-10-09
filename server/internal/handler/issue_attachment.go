package handler

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type IssueAttachmentHandler struct {
	attachmentService *service.IssueAttachmentService
}

func NewIssueAttachmentHandler(attachmentService *service.IssueAttachmentService) *IssueAttachmentHandler {
	return &IssueAttachmentHandler{attachmentService: attachmentService}
}

func (h *IssueAttachmentHandler) Upload(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
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

	c.Header("Content-Disposition", contentDisposition(fileName))
	c.DataFromReader(200, -1, "application/octet-stream", reader, nil)
}

// sanitizeAttachmentFileName 清理 multipart 文件名：取 basename 防路径穿越，
// 去掉双引号、分号、反斜杠与控制字符（它们会破坏 Content-Disposition 或路径语义）。
// 结果为空时返回空串，由 service 兜底默认名。
func sanitizeAttachmentFileName(name string) string {
	base := filepath.Base(name)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == '"' || r == ';' || r == '\\' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, base)
}

// contentDisposition 生成 RFC 6266 的 Content-Disposition 值：兜底 filename 把
// 非 ASCII、控制字符与会破坏 quoted-string 的字符替换为 _；原名含非 ASCII 时
// 按 RFC 5987 追加 filename*=UTF-8”<percent-encoded>。
func contentDisposition(fileName string) string {
	fallback := strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || unicode.IsControl(r) || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, fileName)
	if fallback == "" {
		fallback = "attachment"
	}
	disposition := fmt.Sprintf("attachment; filename=\"%s\"", fallback)
	if !isASCIIString(fileName) {
		disposition += "; filename*=UTF-8''" + rfc5987Encode(fileName)
	}
	return disposition
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// rfc5987Encode 按 RFC 5987 attr-char 规则做 percent 编码：attr-char 之外的
// 字节一律 %XX 大写十六进制（attr-char = 字母数字 + !#$&+-.^_`|~）。
func rfc5987Encode(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '!' || c == '#' || c == '$' || c == '&' || c == '+' || c == '-' ||
			c == '.' || c == '^' || c == '_' || c == '`' || c == '|' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0xF])
		}
	}
	return b.String()
}

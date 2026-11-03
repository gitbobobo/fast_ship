package handler

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// contentEgress 无缓存同源内容出口（INT-182）的唯一实现：issue 附件、issue
// 资产、头像、截图、裁剪、产物下载这些出口都认 ?token=/JWT/run token，交付的
// 是用户可控内容，不能以 Fast Ship 同源文档回给浏览器。鉴权之后的 handler
// 不再各自拼硬化头，只按档调用。
//
// 下载档 Attachment / OctetDownload：nosniff + default-src 'none'; sandbox +
// attachment disposition + 档位 Cache-Control；inline 图片档 InlineImage /
// InlineImageHead：nosniff + CSP 只给 sandbox + inline disposition + 档位
// Cache-Control。
//
// 硬化头与 disposition 也可以只取头不写响应（attachmentHeaders /
// inlineImageHeaders），只留给两条特殊写路径：媒体代理缓存面（Cache-Control
// 由缓存条目 TTL 派生，见 setMediaProxyHardeningHeaders）与 review-agent 截图
// 流（无定长、无缓存头语义，见 ReviewAgentHandler.ScreenshotImage）。
type contentEgress struct{}

// egress 包级出口实例：无状态，handler 直接调用。
var egress contentEgress

// contentEgressCacheControl 档位缓存头。private：这些出口的 URL 都带 token/
// JWT/run token，绝不能进共享缓存层；max-age=300：这些出口没有版本化缓存键
// （头像地址存在用户表里、资产按 ID 固定），内容可能被同名替换，只把复用窗口
// 压在分钟级，别指望它当失效机制。
const contentEgressCacheControl = "private, max-age=300"

// Attachment 下载档：nosniff + default-src 'none'; sandbox + attachment
// disposition（文件名消毒 + RFC 6266/5987）+ 档位 Cache-Control，body 按
// contentType/contentLength 流出（contentLength < 0 表示不定长）。
func (e contentEgress) Attachment(c *gin.Context, fileName, contentType string, contentLength int64, body io.Reader) {
	e.attachmentHeaders(c.Writer.Header(), fileName)
	c.Header("Cache-Control", contentEgressCacheControl)
	c.DataFromReader(http.StatusOK, contentLength, contentType, body, nil)
}

// OctetDownload 下载档的 octet-stream 变体：Content-Type 固定
// application/octet-stream，给落库类型未知/刻意不给类型的附件与产物下载用
// （octet 不进文档上下文，浏览器只会下载）。
func (e contentEgress) OctetDownload(c *gin.Context, fileName string, contentLength int64, body io.Reader) {
	e.Attachment(c, fileName, "application/octet-stream", contentLength, body)
}

// InlineImage inline 图片档：nosniff + CSP 只给 sandbox + Content-Disposition:
// inline + 档位 Cache-Control，body 按 contentType/contentLength 流出
// （contentLength < 0 表示不定长）。保留 inline 是产品行为（「在标签页里看
// 截图」），下载类出口请走 Attachment 档。
func (e contentEgress) InlineImage(c *gin.Context, contentType string, contentLength int64, body io.Reader) {
	e.inlineImageHeaders(c.Writer.Header())
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", contentEgressCacheControl)
	c.DataFromReader(http.StatusOK, contentLength, contentType, body, nil)
}

// InlineImageHead inline 图片档的 HEAD 变体：裁剪预检这类只校验权限/像素预算、
// 不出 body 的请求，头集合与 InlineImage 完全一致。
func (e contentEgress) InlineImageHead(c *gin.Context, contentType string) {
	e.inlineImageHeaders(c.Writer.Header())
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", contentEgressCacheControl)
	c.Data(http.StatusOK, contentType, nil)
}

// attachmentHeaders attachment 档硬化头与 disposition（不含档位 Cache-Control，
// 自带缓存语义的缓存面自己写）。`?token=` 引入的同源内容出口（GitHub 媒体
// 代理、Issue 附件、头像）统一硬化：用户可控内容不能以 Fast Ship 同源文档
// 交付。nosniff 禁止浏览器嗅探改判，CSP sandbox 兜底禁脚本禁同源。
//
// attachment 挡的是顶层导航（「在新标签页打开图片」退化成下载，而不是打开一份
// 同源文档）。子资源不受影响的依据：HTML 的下载处理只写在导航算法里，图片/
// 媒体的加载算法没有 attachment 分支，Fetch 也只把 document/embed/frame/
// iframe/object 列为导航请求；WebKit 的 SubresourceLoader 与 AVFoundation 媒体
// 桥接源码里同样没有按 attachment 下载/拒显的分支。Chromium 已实测；Safari 尚
// 无本机实测，属于源码级判断而不是浏览器验证。不按 Sec-Fetch-Dest 区分：导航
// 请求不止 document（还有 iframe/frame/object/embed），缺失元数据的老客户端
// 还得另定回落，为一个没有证据的 Safari 兼容问题引入两条缓存键与分支，不值得。
func (contentEgress) attachmentHeaders(header http.Header, fileName string) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	header.Set("Content-Disposition", contentDisposition(sanitizeAttachmentFileName(fileName)))
}

// inlineImageHeaders inline 图片档硬化头（不含 disposition/Cache-Control）。
// 给「响应本身就是要在浏览器里当图片看的同源内容」（截图、裁剪、review-agent
// 截图）用：nosniff 禁止把 image/* 改判成 HTML；CSP 只给 `sandbox`——禁脚本、
// 隔离来源，但不设 default-src。
// 不套 default-src 'none'：Chromium 打开图片地址会生成 `<body><img>` 包装文档，
// 那个 img 是否受 img-src（回落到 default-src）约束随版本而异（实测 155 不拦，
// 别的版本可能拦），图片能不能显示不该赌这个实现细节。
func (contentEgress) inlineImageHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox")
}

// ResolveImageMIME 魔数对账：mime 为空/octet-stream（fetch 护栏对判不出类型的
// 持久资产留空，历史缓存里也可能存了非位图类型）时按文件头魔数重判，判得出就
// 给具体类型（有 nosniff 也不会被嗅探利用），判不出保守原样返回。已是具体类型
// 的不重判。
func (contentEgress) ResolveImageMIME(content io.ReaderAt, mime string) string {
	if mime != "" && mime != "application/octet-stream" {
		return mime
	}
	head := make([]byte, 512)
	if n, _ := content.ReadAt(head, 0); n > 0 {
		if sniffed, ok := githubmedia.SniffImageType(head[:n]); ok {
			return sniffed
		}
	}
	return mime
}

// fileExtensionForType 下载名扩展名对账：媒体类型表内直接反查；表外类型（Go
// 嗅探会给 image/x-icon 这类表里没有的 image/*）退而用子类型当后缀（image/
// x-icon → .x-icon），至少让下载名能看出是什么；再不行才落 .bin。附件没有落库
// 文件名，这里只求「下载下来可辨识」，不为个别类型特判简称。
func fileExtensionForType(contentType string) string {
	if ext := githubmedia.MediaFileExtension(contentType); ext != "" {
		return ext
	}
	subtype := contentType
	if idx := strings.Index(contentType, "/"); idx >= 0 {
		subtype = contentType[idx+1:]
	}
	if idx := strings.IndexAny(subtype, "; "); idx >= 0 {
		subtype = subtype[:idx]
	}
	subtype = strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || unicode.IsControl(r) || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, strings.ToLower(subtype))
	if subtype == "" {
		return ".bin"
	}
	return "." + subtype
}

// contentTypeForFileName 头像没有落库 MIME，按扩展名定 Content-Type；未知
// 扩展名保守 octet-stream（只下载不渲染）。
func contentTypeForFileName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// sanitizeAttachmentFileName 清理 multipart 文件名：取 basename 防路径穿越，
// 去掉双引号、分号、反斜杠与控制字符（它们会破坏 Content-Disposition 或路径语义）。
// 结果为空时返回空串：下载路径由 contentDisposition 兜成 filename="attachment"，
// 附件上传路径由 service 兜底默认名。
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

	disposition := "attachment; filename=\"" + fallback + "\""
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

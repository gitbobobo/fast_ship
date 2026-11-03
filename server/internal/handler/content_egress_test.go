package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newContentEgressContext 造一个干净的测试出口上下文。
func newContentEgressContext(t *testing.T, method string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/egress", nil)
	return ctx, rec
}

// 三档出口的头集合：档位决定硬化头、disposition 与档位缓存，调用点不再各拼。
// wantHeaders 是对响应头的完整断言——除 Content-Length 外不允许出现期望之外
// 的头（长度语义单列 wantContentLength：空串表示档位不写长度头）。
func TestContentEgressTierHeaders(t *testing.T) {
	tests := []struct {
		name   string
		method string
		// write 用对应档位方法写出一段完整响应
		write func(c *gin.Context)
		// wantHeaders 必须逐字相等且集合完备
		wantHeaders map[string]string
		// wantContentLength 期望的 Content-Length；空串 = 不得出现该头
		wantContentLength string
		wantBody          string
	}{
		{
			name:   "attachment档_下载语义+完整硬化",
			method: http.MethodGet,
			write: func(c *gin.Context) {
				egress.Attachment(c, "shot.png", "image/png", 4, strings.NewReader("body"))
			},
			wantHeaders: map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "default-src 'none'; sandbox",
				"Content-Disposition":     `attachment; filename="shot.png"`,
				"Cache-Control":           "private, max-age=300",
				"Content-Type":            "image/png",
			},
			wantContentLength: "4",
			wantBody:          "body",
		},
		{
			name:   "octet下载档_与attachment同硬化头_类型固定octet",
			method: http.MethodGet,
			write: func(c *gin.Context) {
				egress.OctetDownload(c, "app.apk", -1, strings.NewReader("bin"))
			},
			wantHeaders: map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "default-src 'none'; sandbox",
				"Content-Disposition":     `attachment; filename="app.apk"`,
				"Cache-Control":           "private, max-age=300",
				"Content-Type":            "application/octet-stream",
			},
			wantContentLength: "",
			wantBody:          "bin",
		},
		{
			name:   "inline图片档_sandbox-only_CSP_不设default-src",
			method: http.MethodGet,
			write: func(c *gin.Context) {
				egress.InlineImage(c, "image/png", 4, strings.NewReader("body"))
			},
			wantHeaders: map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "sandbox",
				"Content-Disposition":     "inline",
				"Cache-Control":           "private, max-age=300",
				"Content-Type":            "image/png",
			},
			wantContentLength: "4",
			wantBody:          "body",
		},
		{
			name:   "inline图片档HEAD变体_头集合与GET一致_无响应体",
			method: http.MethodHead,
			write: func(c *gin.Context) {
				egress.InlineImageHead(c, "image/png")
			},
			wantHeaders: map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "sandbox",
				"Content-Disposition":     "inline",
				"Cache-Control":           "private, max-age=300",
				"Content-Type":            "image/png",
			},
			wantBody: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newContentEgressContext(t, tc.method)
			tc.write(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			// 完备头集合断言：期望头逐字相等（含同名单值），期望之外只允许
			// Content-Length（长度语义单独断言）。
			for key, values := range rec.Header() {
				if key == "Content-Length" {
					continue
				}
				want, ok := tc.wantHeaders[key]
				if !ok {
					t.Fatalf("unexpected header %s = %q", key, values)
				}
				if len(values) != 1 || values[0] != want {
					t.Fatalf("%s = %q, want %q", key, values, want)
				}
			}
			for key, want := range tc.wantHeaders {
				if got := rec.Header().Get(key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if got := rec.Header().Get("Content-Length"); got != tc.wantContentLength {
				t.Fatalf("Content-Length = %q, want %q", got, tc.wantContentLength)
			}
			if body := rec.Body.String(); body != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// 文件名对账收在出口实现里：调用点可以直接传库存名/路径名，消毒与 RFC 6266/
// 5987 由档位方法完成。
func TestContentEgressFileNameReconciliation(t *testing.T) {
	tests := []struct {
		name            string
		fileName        string
		wantDisposition string
	}{
		{
			name:            "裸路径名取basename并消毒",
			fileName:        `../evil"name;x\y.apk`,
			wantDisposition: `attachment; filename="evilnamexy.apk"`,
		},
		{
			// 消毒后为空（整名都是分隔符）落兜底名，不能发出悬空 disposition
			name:            "消毒后为空落兜底名",
			fileName:        "/",
			wantDisposition: `attachment; filename="attachment"`,
		},
		{
			name:     "非ASCII名带RFC5987 filename*",
			fileName: "报告.pdf",
			// 兜底名报→_ 告→_；filename* 为原始名 percent 编码
			wantDisposition: `attachment; filename="__.pdf"; filename*=UTF-8''%E6%8A%A5%E5%91%8A.pdf`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newContentEgressContext(t, http.MethodGet)
			egress.OctetDownload(ctx, tc.fileName, -1, bytes.NewReader(nil))
			if got := rec.Header().Get("Content-Disposition"); got != tc.wantDisposition {
				t.Fatalf("Content-Disposition = %q, want %q", got, tc.wantDisposition)
			}
		})
	}
}

// issue 资产下载名扩展名对账：表内反查、表外子类型兜底、最后 .bin。
func TestFileExtensionForType(t *testing.T) {
	for _, tc := range []struct{ contentType, want string }{
		{"image/png", ".png"},
		{"video/mp4", ".mp4"},
		{"image/x-icon", ".x-icon"},                       // 表外 image/* 用子类型，可辨识即可
		{"application/x-weird;charset=utf-8", ".x-weird"}, // 参数剥掉
		{"IMAGE/X-WEIRD", ".x-weird"},                     // 子类型小写
		{"image/", ".bin"},                                // 子类型消不出字符落 .bin
		{"", ".bin"},
	} {
		if got := fileExtensionForType(tc.contentType); got != tc.want {
			t.Fatalf("fileExtensionForType(%q) = %q, want %q", tc.contentType, got, tc.want)
		}
	}
}

// 头像扩展名 → Content-Type 对账：表内四类，其余保守 octet-stream。
func TestContentTypeForFileName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"a.jpg", "image/jpeg"},
		{"a.JPG", "image/jpeg"},
		{"a.jpeg", "image/jpeg"},
		{"a.png", "image/png"},
		{"a.gif", "image/gif"},
		{"a.webp", "image/webp"},
		{"a.svg", "application/octet-stream"},
		{"noext", "application/octet-stream"},
	} {
		if got := contentTypeForFileName(tc.name); got != tc.want {
			t.Fatalf("contentTypeForFileName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// 魔数对账：空/octet-stream 的兜底 mime 按文件头重判，具体类型不重判，判不出
// 原样保守返回。
func TestContentEgressResolveImageMIME(t *testing.T) {
	pngHead := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 8)...)
	notImage := []byte("plain text, no magic here")

	tests := []struct {
		name    string
		content []byte
		mime    string
		want    string
	}{
		{"具体类型不重判", pngHead, "image/jpeg", "image/jpeg"},
		{"空mime按魔数重判", pngHead, "", "image/png"},
		{"octet按魔数重判", pngHead, "application/octet-stream", "image/png"},
		{"判不出_空mime保持空", notImage, "", ""},
		{"判不出_octet保持octet", notImage, "application/octet-stream", "application/octet-stream"},
		{"空文件_octet保持octet", nil, "application/octet-stream", "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := egress.ResolveImageMIME(bytes.NewReader(tc.content), tc.mime)
			if got != tc.want {
				t.Fatalf("ResolveImageMIME(mime=%q) = %q, want %q", tc.mime, got, tc.want)
			}
		})
	}
}

// 只取硬化头的路径（媒体代理缓存面、review-agent 流式截图）：头集合与档位
// 一致，但不写档位缓存头，也不写响应体——缓存与流式语义由调用方自主决定。
func TestContentEgressHeaderOnlyPaths(t *testing.T) {
	attCtx, attRec := newContentEgressContext(t, http.MethodGet)
	egress.attachmentHeaders(attCtx.Writer.Header(), "clip.mp4")
	if got := attRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q, want nosniff", got)
	}
	if got := attRec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Fatalf("csp = %q, want attachment 档 CSP", got)
	}
	// attachment 头级方法带 disposition（媒体代理按 mediaProxyFileName 对账文件名），
	// 但不带档位缓存头（缓存面 TTL 自派生）。
	if got := attRec.Header().Get("Content-Disposition"); got != `attachment; filename="clip.mp4"` {
		t.Fatalf("Content-Disposition = %q, want attachment; filename=…", got)
	}
	if got := attRec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want absent", got)
	}
	if attRec.Body.Len() != 0 {
		t.Fatalf("body = %d bytes, want 0", attRec.Body.Len())
	}

	inlineCtx, inlineRec := newContentEgressContext(t, http.MethodGet)
	egress.inlineImageHeaders(inlineCtx.Writer.Header())
	if got := inlineRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q, want nosniff", got)
	}
	if got := inlineRec.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("csp = %q, want sandbox-only", got)
	}
	if got := inlineRec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want absent", got)
	}
	if got := inlineRec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want absent", got)
	}
	if inlineRec.Body.Len() != 0 {
		t.Fatalf("body = %d bytes, want 0", inlineRec.Body.Len())
	}
}

package middleware

import (
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/google/uuid"
)

const (
	// ContextKeyRequestID 是请求 ID 在 gin context 中的键；响应头同名回传。
	ContextKeyRequestID = "request_id"
	// RequestIDHeader 是请求 ID 的传递头；代理或调用方可自带，缺省生成 UUID。
	RequestIDHeader = "X-Request-Id"

	ContextKeyRequestLogger = "self_request_logger"

	// 外部传入的请求 ID 上限；超限丢弃并改生成 UUID，避免把凭证形状塞进关联列。
	maxIncomingRequestIDLen = 128
)

// RequestLog 挂载请求日志（INT-94）：生成/透传请求 ID、回写 X-Request-Id、
// 记录方法/路径/路由模板/状态码/耗时；panic 与 5xx 附带堆栈。不记录请求体。
// 中间件自带 recover 并返回统一 500 信封，替代 gin.Recovery 的职责。
func RequestLog(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		requestID := normalizeIncomingRequestID(c.GetHeader(RequestIDHeader))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set(ContextKeyRequestID, requestID)
		c.Writer.Header().Set(RequestIDHeader, requestID)

		logger := base.With(
			slog.String("module", "http"),
			slog.String("request_id", requestID),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
		c.Set(ContextKeyRequestLogger, logger)
		c.Request = c.Request.WithContext(selflog.WithRequestScope(c.Request.Context(), logger, requestID))

		w := &statusCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = w

		panicStack := ""
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					panicStack = string(debug.Stack())
					// 鉴权后 logger 已带 user_id；从 context 再取一次覆盖 panic 路径。
					LoggerFrom(c).Error("请求处理 panic",
						slog.String("panic", panicValueLabel(rec)),
						slog.String("stack", panicStack),
						slog.String("user_id", GetUserID(c)),
					)
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"code":    50000,
						"message": "服务器内部错误",
						"data":    nil,
					})
				}
			}()
			c.Next()
		}()

		completeLog(c, LoggerFrom(c), start, w, panicStack)
	}
}

// BindRequestUser 在鉴权成功后把 user_id 补进请求日志器，使 handler 降级日志
// 与 panic 路径都带归属，不再以空 user_id 当系统日志公开。
func BindRequestUser(c *gin.Context) {
	uid := GetUserID(c)
	if uid == "" {
		return
	}
	logger := LoggerFrom(c).With(
		slog.String("user_id", uid),
		slog.String("auth_type", GetAuthType(c)),
	)
	// 运行令牌端点同时绑定 run_id 与 thread_id，使 trace?run_id=/thread_id=
	// 都能串起 agent 的 HTTP 活动（INT-169 错配审计需要线程定位）。
	if rid := GetRunID(c); rid != "" {
		logger = logger.With(slog.String("run_id", rid))
		if tid := GetRunThreadID(c); tid != "" {
			logger = logger.With(slog.String("thread_id", tid))
		}
	}
	c.Set(ContextKeyRequestLogger, logger)
	c.Request = c.Request.WithContext(selflog.WithRequestScope(c.Request.Context(), logger, RequestIDOf(c)))
}

// completeLog 在请求结束时落一条请求日志；级别按结果分级，5xx/panic 附堆栈。
// 静态资源/SPA 的 2xx 成功请求不入库，避免放大写入量。
func completeLog(c *gin.Context, logger *slog.Logger, start time.Time, w *statusCaptureWriter, panicStack string) {
	status := c.Writer.Status()
	if skipStaticSuccessLog(c, status, panicStack) {
		return
	}
	elapsedMS := float64(time.Since(start).Nanoseconds()) / 1e6

	attrs := []slog.Attr{
		slog.String("route", c.FullPath()),
		slog.Int("status", status),
		slog.Float64("duration_ms", elapsedMS),
	}
	if uid := GetUserID(c); uid != "" {
		attrs = append(attrs, slog.String("user_id", uid))
		attrs = append(attrs, slog.String("auth_type", GetAuthType(c)))
	}
	switch {
	case panicStack != "":
		attrs = append(attrs, slog.String("stack", panicStack))
	case w.failStack != nil:
		attrs = append(attrs, slog.String("stack", string(w.failStack)))
	}

	level := slog.LevelInfo
	if status >= http.StatusInternalServerError || panicStack != "" {
		level = slog.LevelError
	} else if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}

	logger.LogAttrs(c.Request.Context(), level, "请求完成", attrs...)
}

func skipStaticSuccessLog(c *gin.Context, status int, panicStack string) bool {
	if panicStack != "" {
		return false
	}
	if status < 200 || status >= 300 {
		return false
	}
	path := c.Request.URL.Path
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		return false
	}
	return true
}

// LoggerFrom 返回请求作用域 slog.Logger（已带 request_id/module 等字段）；
// 未经过 RequestLog 的调用（单测直连 handler）退化为 stderr 基础日志。
func LoggerFrom(c *gin.Context) *slog.Logger {
	if v, ok := c.Get(ContextKeyRequestLogger); ok {
		if l, ok := v.(*slog.Logger); ok {
			return l
		}
	}
	return fallbackLogger
}

// RequestIDOf 返回当前请求 ID（含调用方自带的 X-Request-Id）。
func RequestIDOf(c *gin.Context) string {
	if v := c.GetString(ContextKeyRequestID); v != "" {
		return v
	}
	return normalizeIncomingRequestID(c.GetHeader(RequestIDHeader))
}

// normalizeIncomingRequestID 限制外部 X-Request-Id：仅 UUID/字母数字/横线，
// 超长或含其它字符则丢弃（由调用方改生成 UUID）。
func normalizeIncomingRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxIncomingRequestIDLen {
		return ""
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return ""
		}
	}
	return raw
}

var fallbackLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func panicValueLabel(rec interface{}) string {
	switch t := rec.(type) {
	case error:
		return t.Error()
	case string:
		return t
	default:
		return "non-string panic value"
	}
}

// statusCaptureWriter 在状态码确定为 5xx 的瞬间抓取当前 goroutine 堆栈。
// c.Next() 返回后再取堆栈只剩中间件帧，抓不到出错的处理函数。
type statusCaptureWriter struct {
	gin.ResponseWriter
	failStack []byte
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	if code >= http.StatusInternalServerError && w.failStack == nil {
		w.failStack = debug.Stack()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCaptureWriter) Write(b []byte) (int, error) {
	if w.Status() >= http.StatusInternalServerError && w.failStack == nil {
		w.failStack = debug.Stack()
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusCaptureWriter) WriteString(s string) (int, error) {
	if w.Status() >= http.StatusInternalServerError && w.failStack == nil {
		w.failStack = debug.Stack()
	}
	return w.ResponseWriter.WriteString(s)
}

// Unwrap 把包装层透明给 http.NewResponseController：生产环境是这层包装
// 让 ResponseController 拿不到真实连接，SSE 的写超时（handler 里的
// SetWriteDeadline）会静默退化为 ErrNotSupported。注意嵌入的是
// gin.ResponseWriter **接口**，接口本身没有 Unwrap，方法不会被提升，
// 必须显式透传；内层 *gin.responseWriter 自带 Unwrap，链路到此畅通。
func (w *statusCaptureWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

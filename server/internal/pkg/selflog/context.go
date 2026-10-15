package selflog

import (
	"context"
	"log/slog"
)

type contextKey int

const (
	ctxLogger contextKey = iota
	ctxRequestID
)

// WithRequestScope 把请求日志器与 request_id 放进 context，供已接收 ctx
// 的服务层（auth、api_key）把业务日志挂到同一条链路上。
func WithRequestScope(ctx context.Context, logger *slog.Logger, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger != nil {
		ctx = context.WithValue(ctx, ctxLogger, logger)
	}
	if requestID != "" {
		ctx = context.WithValue(ctx, ctxRequestID, requestID)
	}
	return ctx
}

// LoggerFromContext 返回请求作用域 slog.Logger；非 HTTP 路径返回 nil。
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(ctxLogger).(*slog.Logger)
	return l
}

// RequestIDFromContext 返回当前请求 ID；没有则空串。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(ctxRequestID).(string)
	return s
}

// LoggerOr 优先用 context 里的请求日志器，否则回落到进程级 logger。
func LoggerOr(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l := LoggerFromContext(ctx); l != nil {
		return l
	}
	return fallback
}

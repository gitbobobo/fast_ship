package selflog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"go.uber.org/zap/zapcore"
)

// zapCore 把既有 zap.Logger 的输出桥接进 selflog 管道（INT-94）。
// zap 服务保持 *zap.Logger 不动，仅在 main 装配时通过 zapcore.NewTee
// 复制一份写库流，避免大面积改写服务层日志调用。
type zapCore struct {
	levels  zapcore.LevelEnabler
	handler slog.Handler
	attrs   []slog.Attr
}

// NewZapCore 返回把 zap 条目转发给 slog handler 的 Core。
func NewZapCore(handler slog.Handler, levels zapcore.LevelEnabler) zapcore.Core {
	return &zapCore{levels: levels, handler: handler}
}

func (c *zapCore) Enabled(lvl zapcore.Level) bool { return c.levels.Enabled(lvl) }

func (c *zapCore) With(fields []zapcore.Field) zapcore.Core {
	clone := &zapCore{levels: c.levels, handler: c.handler}
	clone.attrs = append(clone.attrs, c.attrs...)
	clone.attrs = append(clone.attrs, zapFieldsToAttrs(fields)...)
	return clone
}

func (c *zapCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *zapCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	pc := uintptr(0)
	if ent.Caller.Defined {
		pc = ent.Caller.PC
	}
	record := slog.NewRecord(ent.Time.UTC(), slogLevel(ent.Level), ent.Message, pc)
	if ent.Stack != "" {
		record.AddAttrs(slog.String("stack", ent.Stack))
	}
	record.AddAttrs(c.attrs...)
	record.AddAttrs(zapFieldsToAttrs(fields)...)
	return c.handler.Handle(context.Background(), record)
}

func (c *zapCore) Sync() error { return nil }

func slogLevel(l zapcore.Level) slog.Level {
	switch {
	case l >= zapcore.ErrorLevel:
		return slog.LevelError
	case l >= zapcore.WarnLevel:
		return slog.LevelWarn
	case l >= zapcore.InfoLevel:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

// zapFieldsToAttrs 用 MapObjectEncoder 把 zap 字段拍平成 slog 属性；
// 复杂类型退化为 JSON 字符串，与 slog 附加字段的存储方式一致。
func zapFieldsToAttrs(fields []zapcore.Field) []slog.Attr {
	if len(fields) == 0 {
		return nil
	}
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	attrs := make([]slog.Attr, 0, len(fields))
	for k, v := range enc.Fields {
		attrs = append(attrs, slog.Attr{Key: k, Value: zapValueToSlog(v)})
	}
	return attrs
}

func zapValueToSlog(v interface{}) slog.Value {
	switch t := v.(type) {
	case nil:
		return slog.AnyValue(nil)
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return slog.AnyValue(t)
	case map[string]interface{}, []interface{}:
		// 保留结构化数据，让入库 handler 按 key 递归脱敏；提前字符串化会漏掉内层秘密字段。
		return slog.AnyValue(t)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return slog.StringValue(fmt.Sprintf("%v", t))
		}
		var generic interface{}
		if err := json.Unmarshal(raw, &generic); err == nil {
			return slog.AnyValue(generic)
		}
		return slog.StringValue(string(raw))
	}
}

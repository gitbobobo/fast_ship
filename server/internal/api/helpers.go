package api

import "time"

// Ptr 返回指向 v 的指针。
func Ptr[T any](v T) *T {
	return &v
}

// Deref 返回 p 指向的值；p 为 nil 时返回 T 的零值。
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// NonEmpty 返回指向 s 的指针；s 为空串时返回 nil。
// 生成类型把可选 string 建模为 *string+omitempty，原手写 string+omitempty 的“空串缺省”
// 语义经此函数逐字节保留。
func NonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// True 在 b 为 true 时返回指向 true 的指针，否则返回 nil。用途同 NonEmpty（bool+omitempty）。
func True(b bool) *bool {
	if !b {
		return nil
	}
	v := true
	return &v
}

// JSONTime 复刻 encoding/json 对 time.Time 的 RFC3339Nano 序列化结果。
// 旧手写响应里的 time.Time 字段换成生成类型的 string 字段时，经此函数转换
// 可使 JSON 输出逐字节一致。
func JSONTime(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

// JSONTimePtr 同 JSONTime，作用于 *time.Time；nil 返回 nil。
func JSONTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := JSONTime(*t)
	return &v
}

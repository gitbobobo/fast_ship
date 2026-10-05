package main

import (
	"bytes"
	"os"
	"testing"
)

// TestGeneratedAPIDocUpToDate 重新渲染 api.md 并逐字节比对，
// 防止改了 api/openapi.yaml 后忘了跑 make api-docs——技能文档是代理
// 构造写请求的唯一依据，过期比缺失更糟。
func TestGeneratedAPIDocUpToDate(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	generated, err := render(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	checkedIn, err := os.ReadFile("../../../skills/fast-ship/references/api.md")
	if err != nil {
		t.Fatalf("read checked-in api.md: %v", err)
	}
	if !bytes.Equal([]byte(generated), checkedIn) {
		t.Fatal("skills/fast-ship/references/api.md is stale; run `make api-docs` and commit the result")
	}
}

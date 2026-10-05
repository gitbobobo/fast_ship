package api

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedTypesUpToDate 重新生成 types.gen.go 并逐字节比对，
// 防止改了 api/openapi.yaml 或生成配置后忘了跑 make api-types。
func TestGeneratedTypesUpToDate(t *testing.T) {
	// exec `go tool` 需要编译 oapi-codegen 及其依赖，-short 模式下跳过（离线/无模块缓存环境）。
	if testing.Short() {
		t.Skip("skipping codegen regen check in -short mode")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "types.gen.go")

	// 复制生成配置并把 output 指向临时文件（-o 在 config 带 output 时不生效）。
	cfg, err := os.ReadFile("../../api/oapi-codegen.yaml")
	if err != nil {
		t.Fatalf("read codegen config: %v", err)
	}
	patched := strings.Replace(string(cfg),
		"output: internal/api/types.gen.go", "output: "+out, 1)
	if patched == string(cfg) {
		t.Fatal("codegen config missing `output: internal/api/types.gen.go` line")
	}
	cfgPath := filepath.Join(dir, "oapi-codegen.yaml")
	if err := os.WriteFile(cfgPath, []byte(patched), 0o644); err != nil {
		t.Fatalf("write patched config: %v", err)
	}

	cmd := exec.Command("go", "tool", "oapi-codegen",
		"-config", cfgPath, "../../api/openapi.yaml")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("oapi-codegen failed: %v\n%s", err, combined)
	}

	generated, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read regenerated output: %v", err)
	}
	checkedIn, err := os.ReadFile("types.gen.go")
	if err != nil {
		t.Fatalf("read checked-in types.gen.go: %v", err)
	}
	if !bytes.Equal(generated, checkedIn) {
		t.Fatal("internal/api/types.gen.go is stale; run `make api-types` and commit the result")
	}
}

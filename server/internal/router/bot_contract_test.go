package router

import (
	"os"
	"strings"
	"testing"
)

func TestBotNoMemoryManagementRoutes(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	s := string(raw)
	start := strings.Index(s, `bots := api.Group("/bots"`)
	if start < 0 {
		t.Fatalf("找不到 /api/bots 路由组")
	}
	end := strings.Index(s[start:], "\n\t}")
	if end < 0 {
		end = len(s) - start
	}
	block := s[start : start+end]
	forbidden := []string{
		"/memory", "/memory_files", "/memory-files",
		"/compactions", "/maintenance", "/context-states",
	}
	for _, p := range forbidden {
		if strings.Contains(block, p) {
			t.Fatalf("INT-102 不得新增记忆/维护管理 REST：发现 %s", p)
		}
	}
}

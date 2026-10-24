package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// probeForge 定向测试（INT-166 审查修复）：200 但响应不是预期 JSON 形态按
// failed 处理、不回显 body（实例错误页最大 1MB，回显就是信息泄漏通道）；
// forgeAPIGet 把读体错误当网络错误上抛。

func probeTestServer(t *testing.T, versionBody, userBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(versionBody))
		case "/user":
			_, _ = w.Write([]byte(userBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeForgeHappyPath(t *testing.T) {
	srv := probeTestServer(t, `{"version":"1.22.1"}`, `{"login":"fast-ship-bot"}`)
	probe := probeForge(context.Background(), srv.Client(), srv.URL, "tok")
	if probe.Status != model.DeploymentDetectionOK || probe.Version != "1.22.1" || probe.Username != "fast-ship-bot" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestProbeForgeRejectsNonAPIVersionBody(t *testing.T) {
	// HTML 错误页：解析失败不得把原始 body 当版本回显。
	srv := probeTestServer(t, "<html><body>gateway timeout</body></html>", `{"login":"bot"}`)
	probe := probeForge(context.Background(), srv.Client(), srv.URL, "tok")
	if probe.Status != model.DeploymentDetectionFailed {
		t.Fatalf("status = %s, want failed", probe.Status)
	}
	if !strings.Contains(probe.Detail, "version 响应不是预期") {
		t.Fatalf("detail = %q", probe.Detail)
	}
	if strings.Contains(probe.Detail, "<html>") || strings.Contains(probe.Detail, "gateway timeout") {
		t.Fatalf("body leaked into detail: %q", probe.Detail)
	}
	if probe.Version != "" {
		t.Fatalf("version = %q, want empty", probe.Version)
	}
}

func TestProbeForgeRejectsEmptyVersion(t *testing.T) {
	srv := probeTestServer(t, `{"version":"  "}`, `{"login":"bot"}`)
	probe := probeForge(context.Background(), srv.Client(), srv.URL, "tok")
	if probe.Status != model.DeploymentDetectionFailed || !strings.Contains(probe.Detail, "version 响应不是预期") {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestProbeForgeRejectsNonAPIUserBody(t *testing.T) {
	// user 响应解析不出 login 同样 failed——登录名参与通知文案，空值不该放行。
	srv := probeTestServer(t, `{"version":"1.22.1"}`, `{"message":"not json shape"}`)
	probe := probeForge(context.Background(), srv.Client(), srv.URL, "tok")
	if probe.Status != model.DeploymentDetectionFailed {
		t.Fatalf("status = %s, want failed", probe.Status)
	}
	if !strings.Contains(probe.Detail, "user 响应不是预期") || probe.Username != "" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestProbeForgeTruncatesLongVersion(t *testing.T) {
	long := strings.Repeat("v", 200)
	srv := probeTestServer(t, `{"version":"`+long+`"}`, `{"login":"bot"}`)
	probe := probeForge(context.Background(), srv.Client(), srv.URL, "tok")
	if probe.Status != model.DeploymentDetectionOK {
		t.Fatalf("status = %s, want ok", probe.Status)
	}
	if len([]rune(probe.Version)) != 64 {
		t.Fatalf("version len = %d, want 64", len([]rune(probe.Version)))
	}
}

func TestForgeAPIGetPropagatesBodyReadError(t *testing.T) {
	// 声明 100 字节只写 5 字节就断开：客户端读满声明的长度前连接中断，
	// ReadAll 的错误必须上抛（读失败按网络错误处理），不能当空 body 放行。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(srv.Close)
	_, body, err := forgeAPIGet(context.Background(), srv.Client(), srv.URL+"/version", "tok")
	if err == nil {
		t.Fatalf("expected read error, got body %q", body)
	}
}

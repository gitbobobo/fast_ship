package githubmedia

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestProxyURLVersionMatchesFrontend Go 与前端各自手写一份缓存失效版本常量
// （ProxyURLVersion / MEDIA_PROXY_VERSION），浏览器实际发什么版本由前端那份
// 决定——只涨 Go 侧、漏改前端时没有任何运行时报错，旧 URL 上未硬化的缓存
// 响应会继续命中，属于静默失效。这条测试让漏改直接红。
func TestProxyURLVersionMatchesFrontend(t *testing.T) {
	// 从测试目录向上找仓库根（以 go.mod 所在目录为界）。
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var tsPath string
	for {
		candidate := filepath.Join(dir, "web", "src", "lib", "utils", "github-media-proxy.ts")
		if _, statErr := os.Stat(candidate); statErr == nil {
			tsPath = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到 web/src/lib/utils/github-media-proxy.ts：跨端版本断言必须在仓库检出内运行")
		}
		dir = parent
	}

	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("read %s: %v", tsPath, err)
	}
	re := regexp.MustCompile(`MEDIA_PROXY_VERSION\s*=\s*(\d+)`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("在 %s 中未找到 MEDIA_PROXY_VERSION 常量", tsPath)
	}
	frontend := string(m[1])
	if frontend != ProxyURLVersion {
		t.Fatalf("跨端版本常量不一致：Go ProxyURLVersion = %q，前端 MEDIA_PROXY_VERSION = %q（%s）——只涨一边会让浏览器继续命中旧缓存键", ProxyURLVersion, frontend, tsPath)
	}
	if _, err := strconv.Atoi(frontend); err != nil {
		t.Fatalf("前端版本常量不是数字：%q", frontend)
	}
}

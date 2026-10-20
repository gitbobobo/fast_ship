package catalog

import "testing"

// 内置目录随二进制发布（INT-133）：这里钉住生成产物的基本形状与查询语义，
// 防止脚本或嵌入意外产出空目录/坏索引。
func TestCatalogLoadedAndQueries(t *testing.T) {
	providers := Catalog()
	if len(providers) < 100 {
		t.Fatalf("内置目录应有约 200 家提供商，实得 %d", len(providers))
	}
	for _, id := range []string{"zai", "alibaba", "volcengine", "deepseek", "moonshotai"} {
		if _, ok := FindProvider(id); !ok {
			t.Fatalf("目录应包含 %s", id)
		}
	}
	if _, ok := FindProvider("custom"); ok {
		t.Fatalf("custom 不是目录成员")
	}
	if _, ok := FindProvider("no-such-provider"); ok {
		t.Fatalf("未知 id 不应命中")
	}

	// models 只收 tool_call === true 的条目，窗口/输出缺失为 0（未知）。
	p, ok := FindProvider("zai")
	if !ok || len(p.Models) == 0 {
		t.Fatalf("zai 应有模型清单")
	}
	for _, m := range p.Models {
		if m.ID == "" || m.Name == "" {
			t.Fatalf("模型 id/name 不得为空: %+v", m)
		}
	}

	// 查模型：精确 + 大小写不敏感兜底 + 未命中。
	if _, ok := FindModel("zai", "glm-4.6"); !ok {
		t.Fatalf("zai 应含 glm-4.6")
	}
	if _, ok := FindModel("zai", "GLM-4.6"); !ok {
		t.Fatalf("查模型应大小写不敏感兜底")
	}
	if _, ok := FindModel("zai", "no-such-model"); ok {
		t.Fatalf("未命中不应命中")
	}
	if _, ok := FindModel("custom", "m"); ok {
		t.Fatalf("custom 无目录清单")
	}

	// api 地址反查（存量迁移的目录归属判定）。
	api, ok := ResolveBaseURL("zai")
	if !ok || api == "" {
		t.Fatalf("ResolveBaseURL 应给出目录地址")
	}
	if _, ok := ResolveBaseURL("custom"); ok {
		t.Fatalf("custom 无目录地址")
	}
	id, ok := FindProviderByAPI(api)
	if !ok || id == "" {
		t.Fatalf("按 api 地址应能反查 provider id")
	}
	if _, ok := FindProviderByAPI("https://not-in-catalog.example.com/v1"); ok {
		t.Fatalf("目录外地址不应命中")
	}

	// 规范化与快照时间。
	if got := NormalizeBaseURL(" https://api.example.com/ "); got != "https://api.example.com" {
		t.Fatalf("NormalizeBaseURL: %q", got)
	}
	if GeneratedAt() == "" {
		t.Fatalf("目录应带数据快照时间")
	}
}

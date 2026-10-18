package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
)

// INT-120 机器人工具改进：fs_thread list 真正支持 env_id/query 过滤、
// page_size>1 的分页契约（响应带 offset 自证）、fs_issue get 恒有
// labels/checklist、工具集 v5 冻结 v4。

func TestBotInt120ThreadListQueryAndEnvFilters(t *testing.T) {
	list := &T3ThreadListResponse{
		Items: []T3ThreadSummary{
			{Id: "t-1", Title: "Alpha 修复登录", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-03T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1", ProjectName: "Fast Ship"},
			{Id: "t-2", Title: "beta 写文档", Status: api.T3ThreadSummaryStatusRunning, UpdatedAt: "2026-10-02T00:00:00Z", EnvironmentId: "e2", ProjectId: "p2", ProjectName: "Side Project"},
			{Id: "t-3", Title: "其他杂项", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-01T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1", ProjectName: "Fast Ship"},
		},
		Environments: []api.T3Environment{
			{Id: "e1", Name: "Work", Status: api.T3EnvironmentStatusOk},
			{Id: "e2", Name: "Home", Status: api.T3EnvironmentStatusOk},
		},
	}

	// 零匹配：total_items 必须是 0，不能回退成不过滤。
	out, err := botSlimThreadList(list, &botThreadArgs{Query: "__nonexistent_probe__"})
	if err != nil {
		t.Fatalf("query 零匹配: %v", err)
	}
	if out["total_items"] != 0 || out["returned_items"] != 0 {
		t.Fatalf("零匹配应 total_items=0: %+v", out)
	}
	if items := out["items"].([]map[string]interface{}); len(items) != 0 {
		t.Fatalf("零匹配应无 items: %+v", items)
	}

	// query 命中标题（大小写不敏感）。
	out, err = botSlimThreadList(list, &botThreadArgs{Query: "ALPHA"})
	if err != nil {
		t.Fatalf("query 标题: %v", err)
	}
	items := out["items"].([]map[string]interface{})
	if out["total_items"] != 1 || len(items) != 1 || items[0]["id"] != "t-1" {
		t.Fatalf("query 应不区分大小写命中标题: %+v", out)
	}

	// query 命中项目名。
	out, err = botSlimThreadList(list, &botThreadArgs{Query: "side project"})
	if err != nil {
		t.Fatalf("query 项目名: %v", err)
	}
	items = out["items"].([]map[string]interface{})
	if out["total_items"] != 1 || len(items) != 1 || items[0]["id"] != "t-2" {
		t.Fatalf("query 应命中项目名: %+v", out)
	}

	// query 命中环境名。
	out, err = botSlimThreadList(list, &botThreadArgs{Query: "home"})
	if err != nil {
		t.Fatalf("query 环境名: %v", err)
	}
	items = out["items"].([]map[string]interface{})
	if out["total_items"] != 1 || len(items) != 1 || items[0]["id"] != "t-2" {
		t.Fatalf("query 应命中环境名: %+v", out)
	}

	// env_id 收窄：所有条目都属于该环境，total 收窄，覆盖范围只剩该环境。
	out, err = botSlimThreadList(list, &botThreadArgs{EnvID: "e1"})
	if err != nil {
		t.Fatalf("env_id 过滤: %v", err)
	}
	if out["total_items"] != 2 {
		t.Fatalf("env_id 过滤后应剩 2 条: %+v", out)
	}
	items = out["items"].([]map[string]interface{})
	for _, it := range items {
		if it["environment_id"] != "e1" {
			t.Fatalf("过滤后仍含其他环境的线程: %+v", it)
		}
	}
	envs, ok := out["environments"].([]map[string]interface{})
	if !ok || len(envs) != 1 || envs[0]["id"] != "e1" {
		t.Fatalf("environments 应只剩该环境: %+v", out)
	}
	if _, has := out["incomplete_environments"]; has {
		t.Fatalf("健康环境不应报 incomplete_environments: %+v", out)
	}

	// env_id + query 组合取交集，覆盖范围仍只报该环境。
	out, err = botSlimThreadList(list, &botThreadArgs{EnvID: "e1", Query: "alpha"})
	if err != nil {
		t.Fatalf("env_id+query: %v", err)
	}
	items = out["items"].([]map[string]interface{})
	if out["total_items"] != 1 || len(items) != 1 || items[0]["id"] != "t-1" {
		t.Fatalf("env_id+query 应取交集: %+v", out)
	}
	if envs, _ = out["environments"].([]map[string]interface{}); len(envs) != 1 {
		t.Fatalf("组合过滤时覆盖范围仍应只剩该环境: %+v", out)
	}

	// 未知 env_id：报错，不静默返回空。
	if _, err := botSlimThreadList(list, &botThreadArgs{EnvID: "e-missing"}); err == nil {
		t.Fatalf("未知 env_id 应报错")
	}
}

func TestBotInt120ThreadListPagingNoOverlap(t *testing.T) {
	// page_size=1 时 (page-1)*page_size 恰好等于 page-1，会遮住「按 page-1
	// 做偏移」的错位 bug；这里用 page_size>1 固定分页契约：
	// offset=(page-1)*page_size。如果偏移写成 page-1，这个用例会失败
	// （第 2/3 页会与第 1 页重叠）。
	inputOrder := []string{"t7", "t3", "t1", "t6", "t2", "t5", "t4"} // 故意乱序输入，updated_at 随下标递增
	items := make([]T3ThreadSummary, 0, len(inputOrder))
	for i, id := range inputOrder {
		items = append(items, T3ThreadSummary{
			Id:            id,
			Title:         "线程 " + id,
			Status:        api.T3ThreadSummaryStatusIdle,
			UpdatedAt:     fmt.Sprintf("2026-10-%02dT00:00:00Z", i+1),
			EnvironmentId: "e1",
			ProjectId:     "p1",
		})
	}
	list := &T3ThreadListResponse{Items: items}

	// updated_at 互不相同且随下标递增，降序稳定排序后就是输入的倒序。
	expected := make([]string, 0, len(inputOrder))
	for i := len(inputOrder) - 1; i >= 0; i-- {
		expected = append(expected, inputOrder[i])
	}

	seen := map[string]bool{}
	page := 1
	for start := 0; start < len(expected); start += 3 {
		out, err := botSlimThreadList(list, &botThreadArgs{Page: page, PageSize: 3})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if out["offset"] != start {
			t.Fatalf("page %d offset 应为 %d: %+v", page, start, out)
		}
		wantMore := start+3 < len(expected)
		if out["has_more"] != wantMore {
			t.Fatalf("page %d has_more 应为 %v: %+v", page, wantMore, out)
		}
		got := out["items"].([]map[string]interface{})
		end := start + 3
		if end > len(expected) {
			end = len(expected)
		}
		if len(got) != end-start {
			t.Fatalf("page %d 应返回 %d 条，实得 %d", page, end-start, len(got))
		}
		for i, it := range got {
			id, _ := it["id"].(string)
			if id != expected[start+i] {
				t.Fatalf("page %d 第 %d 条应为 %s，实得 %s", page, i, expected[start+i], id)
			}
			if seen[id] {
				t.Fatalf("page %d 与之前的页重叠：id %s", page, id)
			}
			seen[id] = true
		}
		page++
	}
	if len(seen) != len(expected) {
		t.Fatalf("三页应恰好覆盖全部 %d 条线程，实得 %d 条不同 id", len(expected), len(seen))
	}

	// 越界页：items 为空，offset 收敛到 total，has_more=false。
	out, err := botSlimThreadList(list, &botThreadArgs{Page: 4, PageSize: 3})
	if err != nil {
		t.Fatalf("越界页: %v", err)
	}
	if out["offset"] != len(expected) || out["returned_items"] != 0 || out["has_more"] != false {
		t.Fatalf("越界页应空且 offset=total: %+v", out)
	}
}

func TestBotInt120IssueDetailAlwaysHasLabelsAndChecklist(t *testing.T) {
	// InternalMeta 为 nil：labels/checklist 也要序列化成 []，不是 null 或缺省，
	// 让调用方能区分「没有清单」和「字段没实现」。
	raw, err := json.Marshal(botSlimIssueDetail(&IssueResponse{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"labels", "checklist"} {
		arr, ok := payload[key].([]interface{})
		if !ok || len(arr) != 0 {
			t.Fatalf("%s 应为空数组: %s", key, raw)
		}
	}
	if !strings.Contains(string(raw), `"labels":[]`) || !strings.Contains(string(raw), `"checklist":[]`) {
		t.Fatalf("labels/checklist 必须序列化为 []: %s", raw)
	}

	// 非空时值原样保留。
	detail := &IssueResponse{
		InternalMeta: &IssueInternalMetaResponse{
			Labels:    []api.IssueLabel{{Name: "bug", Color: "ff0000"}},
			Checklist: []api.IssueChecklistItem{{Id: "c1", Title: "验收项", IsCompleted: true, SortOrder: 1}},
		},
	}
	rawFull, err := json.Marshal(botSlimIssueDetail(detail))
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	var full map[string]interface{}
	if err := json.Unmarshal(rawFull, &full); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	labels, ok := full["labels"].([]interface{})
	if !ok || len(labels) != 1 {
		t.Fatalf("labels 应保留: %s", rawFull)
	}
	if label, _ := labels[0].(map[string]interface{}); label["name"] != "bug" {
		t.Fatalf("labels 内容应原样: %+v", labels)
	}
	checklist, ok := full["checklist"].([]interface{})
	if !ok || len(checklist) != 1 {
		t.Fatalf("checklist 应保留: %s", rawFull)
	}
	if item, _ := checklist[0].(map[string]interface{}); item["title"] != "验收项" {
		t.Fatalf("checklist 内容应原样: %+v", checklist)
	}
}

// botToolsetV4FrozenHash 是 INT-120 起冻结的 v4 wire 字节基线（改动前
// HEAD=6e5d8ea 的实测值）。v1–v3 沿用 bot_int103_test.go 的基线常量。
const botToolsetV4FrozenHash = "fdbaa8166785ac0a8c782c60d47f09a188287a0225c80979a2730bb4c009e989"

func TestBotInt120ToolsetV5FreezesV4(t *testing.T) {
	if botToolsetVersion != 6 {
		t.Fatalf("当前工具集版本应为 6，实得 %d", botToolsetVersion)
	}
	// 已发布版本的 wire 字节冻结：v1–v4 的 hash 必须与基线逐字节一致，任何
	// 不等都说明冻结被破坏。
	frozen := map[int64]string{
		1: botToolsetV1FrozenHash,
		2: botToolsetV2FrozenHash,
		3: botToolsetV3FrozenHash,
		4: botToolsetV4FrozenHash,
	}
	for v, want := range frozen {
		if got := botToolsetVersionOf(v).hash; got != want {
			t.Fatalf("v%d tools_hash 偏离冻结基线: got %s want %s", v, got, want)
		}
	}

	v4 := botToolsetVersionOf(4)
	v5 := botToolsetVersionOf(5)
	if v5.hash == v4.hash {
		t.Fatalf("v5 应与 v4 字节不同")
	}

	// v5 自证：fs_thread 的 env_id/query 描述已修正为 list 语义。
	raw5, err := json.Marshal(v5.byName["fs_thread"].Parameters)
	if err != nil {
		t.Fatalf("marshal v5 schema: %v", err)
	}
	if !strings.Contains(string(raw5), "不区分大小写包含匹配") {
		t.Fatalf("v5 fs_thread schema 应带 query 的 list 过滤描述: %s", raw5)
	}
	if !strings.Contains(string(raw5), "list 可选，只保留该环境的线程") {
		t.Fatalf("v5 fs_thread schema 应带 env_id 的 list 过滤描述: %s", raw5)
	}

	// v4 冻结自证：fs_thread 仍是旧描述，未被 v5 污染。
	raw4, err := json.Marshal(v4.byName["fs_thread"].Parameters)
	if err != nil {
		t.Fatalf("marshal v4 schema: %v", err)
	}
	if strings.Contains(string(raw4), "不区分大小写包含匹配") || strings.Contains(string(raw4), "只保留该环境的线程") {
		t.Fatalf("v4 fs_thread schema 不得被 v5 污染: %s", raw4)
	}
}

// TestBotInt120ToolsetChangeNoteHasV5 验证存量会话升到 v5 时收到的是本次变化的
// 说明，而不是回退到 v2 的兑底文案。
func TestBotInt120ToolsetChangeNoteHasV5(t *testing.T) {
	note := botToolsetChangeNote(4, 5)
	for _, want := range []string{"env_id", "query", "offset", "labels"} {
		if !strings.Contains(note, want) {
			t.Fatalf("4→5 升级说明应描述本次变化（缺 %q）: %q", want, note)
		}
	}
	if strings.Contains(note, "fs_memory") {
		t.Fatalf("4→5 不得复用 v2 兑底文案: %q", note)
	}
	// 跨多个边界时只报最新边界，与既有约定一致。
	if v := botToolsetChangeNote(1, 5); v != note {
		t.Fatalf("1→5 应报 v5 文案，实得 %q", v)
	}
}

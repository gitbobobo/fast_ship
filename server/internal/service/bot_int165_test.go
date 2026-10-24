package service

// INT-165 fs_project 搁置测试：工具集 v14（list 新增 include_shelved，默认
// 排除搁置中项目）与冻结基线（v1–v13 hash 不动）、受理校验（include_shelved
// 只对 list 生效、v13 会话不认识该字段）、执行面（默认过滤/include_shelved、
// 瘦身投影带搁置三字段）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// botToolsetV14FrozenHash 是 INT-165 发布的 v14 wire 字节基线：fs_project 的
// include_shelved 参数与搁置描述只进本版（v13 是 INT-160 的 fs_thread wait）；
// 之后任何改动都必须先递增版本。
const botToolsetV14FrozenHash = "97adc09b16b01c9c6c892dbf12966438cd31134ccc09e866f0daf77c16c57dbb"

func TestBotInt165ToolsetV14(t *testing.T) {
	v13 := botToolsetVersionOf(13)
	v14 := botToolsetVersionOf(14)
	if len(v14.schemas) != len(v13.schemas) {
		t.Fatalf("v14 不新增工具: %d vs %d", len(v14.schemas), len(v13.schemas))
	}
	if v14.hash == v13.hash {
		t.Fatalf("v14 与 v13 的 tools_hash 不应相同")
	}
	if v14.hash != botToolsetV14FrozenHash {
		t.Fatalf("v14 tools_hash 偏离基线: got %s want %s", v14.hash, botToolsetV14FrozenHash)
	}
	// v1–v13 各冻结基线原样通过：v14 的演进不得回写任何已发布版本。
	for _, tc := range []struct {
		version int64
		want    string
	}{
		{1, botToolsetV1FrozenHash}, {2, botToolsetV2FrozenHash}, {3, botToolsetV3FrozenHash},
		{4, botToolsetV4FrozenHash}, {5, botToolsetV5FrozenHash}, {6, botToolsetV6FrozenHash},
		{7, botToolsetV7FrozenHash}, {8, botToolsetV8FrozenHash}, {9, botToolsetV9FrozenHash},
		{10, botToolsetV10FrozenHash}, {11, botToolsetV11FrozenHash}, {12, botToolsetV12FrozenHash},
		{13, botToolsetV13FrozenHash},
	} {
		if got := botToolsetVersionOf(tc.version).hash; got != tc.want {
			t.Fatalf("v%d tools_hash 偏离冻结基线: got %s want %s", tc.version, got, tc.want)
		}
	}
	// v4–v13 的 fs_project 冻结在 v4–v13 的共同形态；v14 用活跃结构体。
	for _, version := range []int64{4, 5, 9, 12, 13} {
		project := botToolsetVersionOf(version).byName["fs_project"]
		if project.ArgsType.Name() != "botProjectArgsV13Frozen" {
			t.Fatalf("v%d fs_project 应冻结在 botProjectArgsV13Frozen，实得 %s", version, project.ArgsType.Name())
		}
	}
	project := v14.byName["fs_project"]
	if project.ArgsType.Name() != "botProjectArgs" {
		t.Fatalf("v14 fs_project 应使用活跃参数结构体，实得 %s", project.ArgsType.Name())
	}
	if len(project.Actions) != 4 {
		t.Fatalf("v14 fs_project 应仍是 4 个动作，实得 %d", len(project.Actions))
	}
	// 工具与 list 动作描述写明搁置过滤语义。
	for _, want := range []string{"搁置", "include_shelved"} {
		if !strings.Contains(project.Description, want) {
			t.Fatalf("v14 fs_project 描述应含 %q: %s", want, project.Description)
		}
	}
	// 当前发布版本就是 v14。
	if current := botToolsetOf(); current.version != 14 {
		t.Fatalf("botToolsetOf 应指向 v14，实得 v%d", current.version)
	}
}

// 受理校验：include_shelved 只对 list 生效；v13 会话的冻结 schema 不认识该
// 字段，携带即拒（保持 v13 的参数面不变）。
func TestBotInt165IncludeShelvedAcceptance(t *testing.T) {
	v13 := botToolsetVersionOf(13)
	v14 := botToolsetVersionOf(14)

	if _, err := botPrepareToolCall(v14, "fs_project", `{"action":"list","include_shelved":true}`); err != nil {
		t.Fatalf("v14 list 携带 include_shelved 应被受理: %v", err)
	}
	if _, err := botPrepareToolCall(v14, "fs_project", `{"action":"list","include_shelved":false}`); err != nil {
		t.Fatalf("v14 list 显式 false 同样受理: %v", err)
	}
	for _, args := range []string{
		`{"action":"get","id":"p1","include_shelved":true}`,
		`{"action":"create","name":"n","include_shelved":true}`,
		`{"action":"update","id":"p1","include_shelved":true}`,
	} {
		if _, err := botPrepareToolCall(v14, "fs_project", args); err == nil {
			t.Fatalf("v14 非.list 动作携带 include_shelved 应被拒绝: %s", args)
		}
	}
	if _, err := botPrepareToolCall(v13, "fs_project", `{"action":"list","include_shelved":true}`); err == nil {
		t.Fatalf("v13 会话不认识 include_shelved，携带应被拒绝")
	}
	// v13 的 list 不带新字段照常受理。
	if _, err := botPrepareToolCall(v13, "fs_project", `{"action":"list","page":1}`); err != nil {
		t.Fatalf("v13 list 应照常受理: %v", err)
	}
}

// 执行面：fs_project list 默认排除搁置中项目，include_shelved=true 返回全部；
// get 对搁置项目照常可用并带搁置字段。
func TestBotInt165ProjectListFiltersShelved(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int165list")
	bot := env.createBot(t, userID)

	active, err := env.bots.projects.Create(userID, &CreateProjectRequest{Name: "活跃项目"})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	shelvedRow, err := env.bots.projects.Create(userID, &CreateProjectRequest{Name: "搁置项目"})
	if err != nil {
		t.Fatalf("create shelved: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	if err := env.db.Model(&model.Project{}).Where("id = ?", shelvedRow.Id).
		Update("shelved_at", past).Error; err != nil {
		t.Fatalf("shelve: %v", err)
	}

	list := func(args string) (items []map[string]interface{}, total int) {
		t.Helper()
		raw, err := env.bots.execProjectRead(context.Background(), &model.BotToolCall{
			ID: "call-" + uuid.NewString()[:8], BotID: bot.ID, ToolName: "fs_project",
			Action: "list", ArgumentsJSON: args,
		})
		if err != nil {
			t.Fatalf("fs_project list(%s): %v", args, err)
		}
		var page struct {
			Items []map[string]interface{} `json:"items"`
			Total int                      `json:"total"`
		}
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatalf("decode list result: %v", err)
		}
		return page.Items, page.Total
	}

	defaultItems, defaultTotal := list(`{"action":"list","page":1,"page_size":20}`)
	if defaultTotal != 1 || len(defaultItems) != 1 || defaultItems[0]["id"] != active.Id {
		t.Fatalf("默认口径应只含活跃项目: total=%d items=%+v", defaultTotal, defaultItems)
	}

	allItems, allTotal := list(`{"action":"list","page":1,"page_size":20,"include_shelved":true}`)
	if allTotal != 2 || len(allItems) != 2 {
		t.Fatalf("include_shelved=true 应返回全部项目: total=%d items=%+v", allTotal, allItems)
	}
	var shelvedSlim map[string]interface{}
	for _, item := range allItems {
		if item["id"] == shelvedRow.Id {
			shelvedSlim = item
		}
	}
	if shelvedSlim == nil || shelvedSlim["is_shelved"] != true || shelvedSlim["shelved_at"] == nil || shelvedSlim["shelved_until"] != nil {
		t.Fatalf("搁置项目瘦身投影应带搁置字段: %+v", shelvedSlim)
	}

	// get 对搁置项目照常可用（读写不受搁置影响）。
	raw, err := env.bots.execProjectRead(context.Background(), &model.BotToolCall{
		ID: "call-" + uuid.NewString()[:8], BotID: bot.ID, ToolName: "fs_project",
		Action: "get", ArgumentsJSON: `{"action":"get","id":"` + shelvedRow.Id + `"}`,
	})
	if err != nil {
		t.Fatalf("fs_project get: %v", err)
	}
	if !strings.Contains(raw, `"is_shelved":true`) {
		t.Fatalf("get 搁置项目应带 is_shelved: %s", raw)
	}
}

// 升级通知档位（F2）：v14 讲搁置过滤、v13 讲 wait，跨档逐级级联，不落
// fs_memory 兜底句——当前线上多为 v13，激活到 v14 时缺档会收到与本次升级
// 无关的兜底说明。
func TestBotInt165ToolsetChangeNote(t *testing.T) {
	note1314 := botToolsetChangeNote(13, 14)
	for _, want := range []string{"搁置", "include_shelved"} {
		if !strings.Contains(note1314, want) {
			t.Fatalf("13→14 升级通知应含 %q: %s", want, note1314)
		}
	}
	// 13→14 只讲 v14：不重告 v13 的 wait，也不落 fs_memory 兜底句。
	if strings.Contains(note1314, "wait") || strings.Contains(note1314, "fs_memory") {
		t.Fatalf("13→14 不应含 wait 语义或 fs_memory 兜底句: %s", note1314)
	}

	note1214 := botToolsetChangeNote(12, 14)
	for _, want := range []string{"搁置", "wait", "not_started"} {
		if !strings.Contains(note1214, want) {
			t.Fatalf("12→14 应级联 v13 wait 语义并含 %q: %s", want, note1214)
		}
	}
	if strings.Contains(note1214, "fs_memory") {
		t.Fatalf("12→14 不应落 fs_memory 兜底句: %s", note1214)
	}

	// 11→14 级联到 v12 的执行配置告知。
	note1114 := botToolsetChangeNote(11, 14)
	for _, want := range []string{"搁置", "wait", "model_selection", "runtime_mode", "workspace"} {
		if !strings.Contains(note1114, want) {
			t.Fatalf("11→14 应级联 v12 执行配置语义并含 %q: %s", want, note1114)
		}
	}

	// 用户白话版逐档并列。
	if user := botToolsetUserNote(13, 14); !strings.Contains(user, "搁置") {
		t.Fatalf("13→14 用户说明应提到搁置: %s", user)
	}
	for _, want := range []string{"搁置", "等待"} {
		if user := botToolsetUserNote(12, 14); !strings.Contains(user, want) {
			t.Fatalf("12→14 用户说明应级联 v13 档并含 %q: %s", want, user)
		}
	}
	if user := botToolsetUserNote(11, 14); !strings.Contains(user, "执行配置") {
		t.Fatalf("11→14 用户说明应级联 v12 执行配置档: %s", user)
	}
}

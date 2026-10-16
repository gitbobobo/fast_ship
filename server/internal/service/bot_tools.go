package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/invopop/jsonschema"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/shared"
)

// 白名单工具：固定 10 个工具、29 个 action。所有业务写操作均为
// async + 用户确认/已有授权；同步读取也记调用账本。不做反射调用 service、
// 不提供任意 RPC。参数使用专用结构体，action 在 schema 中列 enum，服务端
// 再校验该 action 的必填、禁止字段。

type botActionDef struct {
	Name                 string
	Description          string
	Mode                 model.BotToolExecutionMode
	RequiresConfirmation bool
}

type botToolDef struct {
	Name        string
	Description string
	// ArgsType 生成 Parameters；进程内一次，不按机器人权限动态删减或排序。
	ArgsType   reflect.Type
	Parameters shared.FunctionParameters
	Actions    []botActionDef
}

// botArgsHelp / botArgsResult / botArgsStop / botArgsCancel 等零字段工具
// 共用空参数对象。
type botNoArgs struct{}

type botScheduleArgs struct {
	Action string `json:"action" jsonschema:"enum=status,required"`
}

type botHelpArgs struct{}

type botResultArgs struct {
	Action string `json:"action" jsonschema:"enum=read,required"`
	CallID string `json:"call_id" jsonschema:"description=要读取的调用 ID（必填，required）"`
	// Offset/Limit 以 Unicode 字符数计，避免切断 UTF-8。
	Offset int `json:"offset,omitempty" jsonschema:"description=起始字符偏移（默认 0）"`
	Limit  int `json:"limit,omitempty" jsonschema:"description=本次读取的字符数（默认 4000，上限 16000）"`
}

type botStopArgs struct{}

type botCancelArgs struct {
	Action string `json:"action" jsonschema:"enum=cancel,required"`
	CallID string `json:"call_id" jsonschema:"description=要取消的本机器人后台调用 ID（必填，required）"`
}

type botProjectArgs struct {
	Action        string  `json:"action" jsonschema:"enum=list,get,create,update,required"`
	ID            string  `json:"id,omitempty" jsonschema:"description=项目 ID（get/update 必填）"`
	Page          int     `json:"page,omitempty" jsonschema:"description=页码（从 1 起，list 用）"`
	PageSize      int     `json:"page_size,omitempty" jsonschema:"description=页长（1-100，list 用）"`
	Name          string  `json:"name,omitempty" jsonschema:"description=项目名（create 必填；update 可改）"`
	Description   *string `json:"description,omitempty" jsonschema:"description=项目描述；update 提供空串即清空"`
	RepositoryURL string  `json:"repository_url,omitempty" jsonschema:"description=仓库 URL（http/ssh）"`
}

// botIssueArgs 是 fs_issue 的参数超集：schema 描述并集，服务端按 action
// 校验必填/禁止字段（botValidateActionFields）。
type botIssueArgs struct {
	Action string `json:"action" jsonschema:"enum=list,get,comments,timeline,create,update,set_workflow,replace_checklist,comment,required"`
	// list / create 用
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=Fast Ship 项目 ID（list/create 必填）"`
	// 读取与写操作共用
	IssueID  string `json:"issue_id,omitempty" jsonschema:"description=Issue ID（get/comments/timeline 及全部写操作必填）"`
	Page     int    `json:"page,omitempty" jsonschema:"description=页码（从 1 起）"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"description=页长（1-100）"`
	State    string `json:"state,omitempty" jsonschema:"description=状态过滤（open/closed）或目标状态（update）"`
	Query    string `json:"query,omitempty" jsonschema:"description=关键词过滤"`
	Label    string `json:"label,omitempty" jsonschema:"description=标签过滤"`
	Source   string `json:"source,omitempty" jsonschema:"description=internal/github（list 过滤或 create 来源；create 缺省 internal）"`
	Workflow string `json:"workflow,omitempty" jsonschema:"description=工作流状态过滤"`
	Sort     string `json:"sort,omitempty" jsonschema:"description=排序"`
	// create / update 用
	Title       string    `json:"title,omitempty" jsonschema:"description=标题（create 必填；update 提供即替换）"`
	Body        *string   `json:"body,omitempty" jsonschema:"description=正文（Markdown）；create 缺省为空，update 提供即替换、省略即保留"`
	Labels      *[]string `json:"labels,omitempty" jsonschema:"description=标签整组替换（仅 GitHub）"`
	StateReason *string   `json:"state_reason,omitempty" jsonschema:"description=completed/not_planned/reopened（state=closed 时）"`
	// set_workflow 用
	WorkflowStatus string `json:"workflow_status,omitempty" jsonschema:"description=工作流状态（set_workflow 必填）"`
	// replace_checklist 用：整组替换，确认卡片会明确展示
	Items []IssueChecklistItemInput `json:"items,omitempty" jsonschema:"description=完整清单（replace_checklist 必填，整组替换）"`
	// comment 用
	CommentBody string `json:"comment_body,omitempty" jsonschema:"description=评论正文（comment 必填）"`
}

// botThreadArgs 是 fs_thread 的参数超集。
type botThreadArgs struct {
	Action    string `json:"action" jsonschema:"enum=list,get,item,composer_options,workspace_options,launch,send,stop,required"`
	EnvID     string `json:"env_id,omitempty" jsonschema:"description=T3 环境 ID（除 list 外必填）"`
	ThreadID  string `json:"thread_id,omitempty" jsonschema:"description=T3 线程 ID（get/item/send/stop 必填）"`
	ItemID    string `json:"item_id,omitempty" jsonschema:"description=T3 线程条目 ID（item 必填）"`
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=T3 项目 ID（workspace_options/launch 必填）"`
	Query     string `json:"query,omitempty" jsonschema:"description=引用名过滤（workspace_options）"`
	Cursor    *int   `json:"cursor,omitempty" jsonschema:"description=上一页返回的 next_cursor（workspace_options）"`
	Text      string `json:"text,omitempty" jsonschema:"description=消息正文（launch/send 必填）"`
	Title     string `json:"title,omitempty" jsonschema:"description=线程标题（launch 可选，缺省由 T3 生成）"`
}

type botEnvironmentArgs struct {
	Action string `json:"action" jsonschema:"enum=list,projects,required"`
}

// botToolRegistry 按固定顺序注册全部工具；顺序即协议 tools 数组顺序，
// 全局 toolset_version 提升后才变化。
func botToolRegistry() []*botToolDef {
	return []*botToolDef{
		{
			Name:        "fs_project",
			Description: "操作 Fast Ship 项目：list/get 读取，create/update 为需确认的写操作（首期只开放名称、描述、仓库 URL）。",
			ArgsType:    reflect.TypeOf(botProjectArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出当前用户项目", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取项目详情", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建项目", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新项目（名称/描述/仓库 URL）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_issue",
			Description: "操作 Fast Ship Issue：list/get/comments/timeline 读取，create/update/set_workflow/replace_checklist/comment 为需确认的写操作。replace_checklist 是整组替换。",
			ArgsType:    reflect.TypeOf(botIssueArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "按项目列出 Issue", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取 Issue 详情", Mode: model.BotExecutionSync},
				{Name: "comments", Description: "读取 Issue 评论", Mode: model.BotExecutionSync},
				{Name: "timeline", Description: "读取 Issue 时间线", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建 Issue（内部或 GitHub）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新 Issue 标题/正文/状态/标签", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "set_workflow", Description: "设置工作流状态", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "replace_checklist", Description: "整组替换验收清单", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "comment", Description: "发表 Issue 评论", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_thread",
			Description: "操作已连接 T3 环境里的线程：list/get/item/composer_options/workspace_options 读取，launch/send/stop 为需确认的写操作。launch 只表示启动 RPC 受理，不代表远端完成。",
			ArgsType:    reflect.TypeOf(botThreadArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出全部环境的线程", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取线程详情", Mode: model.BotExecutionSync},
				{Name: "item", Description: "读取线程条目", Mode: model.BotExecutionSync},
				{Name: "composer_options", Description: "读取启动器选项", Mode: model.BotExecutionSync},
				{Name: "workspace_options", Description: "读取工作区/引用选项", Mode: model.BotExecutionSync},
				{Name: "launch", Description: "在新线程中启动 T3 任务", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "send", Description: "向既有线程发消息", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "stop", Description: "停止远端线程的当前运行", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_environment",
			Description: "读取已连接的 T3 环境与其项目。首期不开放配对、密钥代答与环境删除。",
			ArgsType:    reflect.TypeOf(botEnvironmentArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出已连接环境", Mode: model.BotExecutionSync},
				{Name: "projects", Description: "发现各环境的 T3 项目", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_schedule",
			Description: "定时调度能力状态查询（首期不可用）。",
			ArgsType:    reflect.TypeOf(botScheduleArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询调度能力是否可用", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory",
			Description: "跨会话记忆能力状态查询（首期不可用）。",
			ArgsType:    reflect.TypeOf(botScheduleArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询记忆能力是否可用", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_help",
			Description: "读取嵌入程序的工具使用说明。",
			ArgsType:    reflect.TypeOf(botHelpArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取使用说明", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_result",
			Description: "按 call_id 分页读取被截断的完整工具结果（offset/limit 以 Unicode 字符计）。",
			ArgsType:    reflect.TypeOf(botResultArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取完整结果", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_stop",
			Description: "结束当前主线运行（等效用户点击停止）。",
			ArgsType:    reflect.TypeOf(botStopArgs{}),
			Actions: []botActionDef{
				{Name: "stop", Description: "停止当前 run", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_cancel",
			Description: "取消本机器人的指定后台调用。",
			ArgsType:    reflect.TypeOf(botCancelArgs{}),
			Actions: []botActionDef{
				{Name: "cancel", Description: "取消后台调用", Mode: model.BotExecutionSync},
			},
		},
	}
}

var (
	botToolsetOnce   sync.Once
	botToolsetLoaded *botToolsetHolder
)

type botToolsetHolder struct {
	tools    []*botToolDef
	byName   map[string]*botToolDef
	byAction map[string]botActionDef
	schemas  []openai.ChatCompletionToolUnionParam
	// hash / bytes 是 tools 数组的 SHA-256 与序列化字节数（估算占用用）。
	hash  string
	bytes int
}

// botToolsetOf 懒加载进程级工具集（sync.Once 串行化首次构造，避免并发
// 首轮的数据竞争与半可见状态）；schema 生成失败属于编程错误，直接 panic。
func botToolsetOf() *botToolsetHolder {
	botToolsetOnce.Do(func() { botToolsetLoaded = botBuildToolset() })
	return botToolsetLoaded
}

func botBuildToolset() *botToolsetHolder {
	tools := botToolRegistry()
	holder := &botToolsetHolder{
		tools:    tools,
		byName:   map[string]*botToolDef{},
		byAction: map[string]botActionDef{},
	}
	reflector := &jsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
		ExpandedStruct: true,
	}
	var rawAll []byte
	for _, tool := range tools {
		schema := reflector.ReflectFromType(tool.ArgsType)
		schema.ID = ""
		schema.Version = ""
		schema.Definitions = nil
		schema.Description = tool.Description
		raw, err := schema.MarshalJSON()
		if err != nil {
			panic(fmt.Sprintf("bot tool schema %s: %v", tool.Name, err))
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			panic(fmt.Sprintf("bot tool schema %s: %v", tool.Name, err))
		}
		tool.Parameters = shared.FunctionParameters(parsed)
		rawAll = append(rawAll, raw...)
		holder.byName[tool.Name] = tool
		for _, action := range tool.Actions {
			holder.byAction[tool.Name+"/"+action.Name] = action
		}
		holder.schemas = append(holder.schemas, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        tool.Name,
					Description: param.NewOpt(tool.Description),
					Parameters:  tool.Parameters,
				},
			},
		})
	}
	sum := sha256.Sum256(rawAll)
	holder.hash = hex.EncodeToString(sum[:])
	holder.bytes = len(rawAll)
	return holder
}

// botToolsTokenEstimate 工具定义的 token 占用估算：chars/4（估算值，标注估算方式）。
func botToolsTokenEstimate() (int64, string) {
	ts := botToolsetOf()
	return int64(ts.bytes) / 4, "chars/4"
}

// ---------- 调用准备：解析、规范化、校验、范围解析 ----------

// botPreparedCall 是模型工具调用经服务端校验后的落库形态。
type botPreparedCall struct {
	ToolName             string
	Action               string
	ArgumentsJSON        string
	ArgumentsHash        string
	Mode                 model.BotToolExecutionMode
	RequiresConfirmation bool
	// Scope 是确认卡片与授权规则的骨架。
	ScopeKind     string
	ScopeID       string
	EnvironmentID *string
	// FieldsConstraint 是 update 类动作允许写入的字段集合（写进授权约束）。
	FieldsConstraint []string
	// ParamConstraint 是参数值级约束（如 source=github）：同一动作不同参数
	// 值的副作用不同，「总是允许」必须按值绑定，不能只看字段集合。
	ParamConstraint string
}

// botPrepareToolCall 解析并校验一次模型工具调用；任何失败都返回 AppError，
// 由调用方以同步失败回复模型（不建确认卡片）。
func botPrepareToolCall(toolName, rawArguments string) (*botPreparedCall, error) {
	ts := botToolsetOf()
	tool, ok := ts.byName[toolName]
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, "未知工具: "+toolName)
	}

	// 拒绝未知字段：schema additionalProperties=false，这里做服务端兜底。
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawArguments), &rawMap); err != nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, "工具参数不是有效 JSON 对象")
	}
	knownKeys := botArgKeys(tool.ArgsType)
	for key := range rawMap {
		if !knownKeys[key] {
			return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("工具 %s 不接受参数 %s", toolName, key))
		}
	}

	// 规范化：重新按结构体序列化，字段顺序与缺省值固定。
	args := reflect.New(tool.ArgsType).Interface()
	if err := json.Unmarshal([]byte(rawArguments), args); err != nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, "工具参数类型不符: "+err.Error())
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, errs.ErrInternal
	}
	sum := sha256.Sum256(normalized)

	prepared := &botPreparedCall{
		ToolName:      toolName,
		ArgumentsJSON: string(normalized),
		ArgumentsHash: hex.EncodeToString(sum[:]),
	}

	// 解出 action 并校验。
	actionVal := reflect.ValueOf(args).Elem().FieldByName("Action")
	if !actionVal.IsValid() {
		// 无 action 字段的工具：action 即工具唯一动作。
		if len(tool.Actions) != 1 {
			return nil, errs.ErrInternal
		}
		prepared.Action = tool.Actions[0].Name
	} else {
		prepared.Action = strings.TrimSpace(actionVal.String())
	}
	meta, ok := ts.byAction[toolName+"/"+prepared.Action]
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("工具 %s 不支持 action %s", toolName, prepared.Action))
	}
	prepared.Mode = meta.Mode
	prepared.RequiresConfirmation = meta.RequiresConfirmation

	if err := botValidateActionFields(toolName, prepared.Action, rawMap); err != nil {
		return nil, err
	}
	return prepared, nil
}

// botArgKeys 收集参数结构体声明的 JSON 键。
func botArgKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// botValidateActionFields 按工具+action 校验必填/禁止字段。
func botValidateActionFields(toolName, action string, raw map[string]json.RawMessage) error {
	has := func(key string) bool {
		v, ok := raw[key]
		return ok && string(v) != "null"
	}
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(raw[key], &s)
		return strings.TrimSpace(s)
	}
	bad := func(msg string) error {
		return errs.New(errs.ErrInvalidParams.Code, msg)
	}
	switch toolName + "/" + action {
	case "fs_project/list":
	case "fs_project/get", "fs_project/update":
		if !has("id") {
			return bad("fs_project " + action + " 需要 id")
		}
	case "fs_project/create":
		if !has("name") || str("name") == "" {
			return bad("fs_project create 需要 name")
		}
		if has("id") {
			return bad("fs_project create 不接受 id")
		}
	case "fs_issue/list":
		if !has("project_id") {
			return bad("fs_issue list 需要 project_id")
		}
	case "fs_issue/get", "fs_issue/comments", "fs_issue/timeline",
		"fs_issue/update", "fs_issue/set_workflow", "fs_issue/replace_checklist", "fs_issue/comment":
		if !has("issue_id") {
			return bad("fs_issue " + action + " 需要 issue_id")
		}
		if action == "comment" && (!has("comment_body") || str("comment_body") == "") {
			return bad("fs_issue comment 需要 comment_body")
		}
		if action == "set_workflow" && !has("workflow_status") {
			return bad("fs_issue set_workflow 需要 workflow_status")
		}
		if action == "replace_checklist" && !has("items") {
			return bad("fs_issue replace_checklist 需要 items（整组替换）")
		}
	case "fs_issue/create":
		if !has("project_id") {
			return bad("fs_issue create 需要 project_id")
		}
		if !has("title") || str("title") == "" {
			return bad("fs_issue create 需要 title")
		}
	case "fs_thread/list":
	case "fs_thread/get", "fs_thread/item":
		if !has("env_id") || !has("thread_id") {
			return bad("fs_thread " + action + " 需要 env_id 与 thread_id")
		}
		if action == "item" && !has("item_id") {
			return bad("fs_thread item 需要 item_id")
		}
	case "fs_thread/composer_options":
		if !has("env_id") {
			return bad("fs_thread composer_options 需要 env_id")
		}
	case "fs_thread/workspace_options":
		if !has("env_id") || !has("project_id") {
			return bad("fs_thread workspace_options 需要 env_id 与 project_id")
		}
	case "fs_thread/launch":
		if !has("env_id") || !has("project_id") || !has("text") {
			return bad("fs_thread launch 需要 env_id、project_id 与 text")
		}
	case "fs_thread/send":
		if !has("env_id") || !has("thread_id") || !has("text") {
			return bad("fs_thread send 需要 env_id、thread_id 与 text")
		}
	case "fs_thread/stop":
		if !has("env_id") || !has("thread_id") {
			return bad("fs_thread stop 需要 env_id 与 thread_id")
		}
	case "fs_environment/list", "fs_environment/projects":
	case "fs_schedule/status", "fs_memory/status", "fs_help/read", "fs_stop/stop":
	case "fs_result/read":
		if !has("call_id") {
			return bad("fs_result read 需要 call_id")
		}
	case "fs_cancel/cancel":
		if !has("call_id") {
			return bad("fs_cancel cancel 需要 call_id")
		}
	default:
		return bad("不支持的工具动作: " + toolName + "/" + action)
	}
	return nil
}

// ---------- 执行 ----------

// botExecuteSyncTool 在主线循环内执行同步工具：读取类、能力查询、帮助、
// 结果续读、停止与取消。返回进入协议回复的内容（JSON 字符串）。
func (s *BotService) botExecuteSyncTool(ctx context.Context, call *model.BotToolCall) (string, error) {
	switch call.ToolName + "/" + call.Action {
	case "fs_project/list", "fs_project/get":
		return s.execProjectRead(ctx, call)
	case "fs_issue/list", "fs_issue/get", "fs_issue/comments", "fs_issue/timeline":
		return s.execIssueRead(ctx, call)
	case "fs_thread/list", "fs_thread/get", "fs_thread/item", "fs_thread/composer_options", "fs_thread/workspace_options":
		return s.execThreadRead(ctx, call)
	case "fs_environment/list", "fs_environment/projects":
		return s.execEnvironmentRead(ctx, call)
	case "fs_schedule/status", "fs_memory/status":
		return s.execCapabilityStatus(call)
	case "fs_help/read":
		// 帮助文本包成 JSON：结果会以 json.RawMessage 透出到调用列表与
		// 快照，裸文本会让整个响应无法序列化。
		return marshalJSONString(map[string]string{"text": botHelpText}), nil
	case "fs_result/read":
		return s.execResultRead(call)
	case "fs_stop/stop":
		return s.execStopCurrentRun(call)
	case "fs_cancel/cancel":
		return s.execCancelOwnCall(call)
	default:
		return "", errs.New(errs.ErrInvalidParams.Code, "同步工具不执行该动作: "+call.ToolName+"/"+call.Action)
	}
}

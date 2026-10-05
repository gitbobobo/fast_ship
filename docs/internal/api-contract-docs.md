# API 契约文档

契约真相源是手写的单文件 `server/api/openapi.yaml`。Go 类型和技能参考都从它生成，字段表不再维护第二份。

## 真相源

`server/api/openapi.yaml` 覆盖 `server/internal/router/router.go` 注册的全部 `/api` 路由。组件 schema 用文件内 `$ref`，不拆成多个文件，也不引用外部文件。

## 生成链路

两条命令，输入都是这份 yaml。

`make api-types` 用 oapi-codegen 写出 `server/internal/api/types.gen.go`。配置在 `server/api/oapi-codegen.yaml`，版本由 `server/go.mod` 的 `tool` 指令钉死（当前是 `github.com/oapi-codegen/oapi-codegen/v2 v2.8.0`）。生成文件提交进 git。`go test` 会再跑一次生成，并断言结果和已提交文件逐字节一致。

`make api-docs` 运行 `server/cmd/apidocgen`（在 `server` 目录下 `go run ./cmd/apidocgen`），写出 `skills/fast-ship/references/api.md`。同一份 yaml 两次运行，markdown 逐字节相同；`go test` 断言已提交文件与重新渲染结果一致。文件头注明请勿手改。

直接调 codegen 的命令写在 `server/api/oapi-codegen.yaml` 顶部：`cd server && go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml`。

## 生成覆盖不到的部分

`apidocgen` 按端点把 spec 里的 description 展开进 api.md。下面这些仍写在 `skills/fast-ship/SKILL.md`，因为代理要在构造请求之前就看到，而 spec 的 security 数组表达不了 handler 里的字段级拒绝：

- multipart 端点是表单上传，不是 JSON。Issue 图片响应里的 `markdown` 要回填正文。
- 字段级权限。API Key 不能改 `state` / `state_reason`、不能发评论、不能写 ship-hook。collab 和 recommendation 的 PUT，以及 `POST /projects/{id}/logs`，只接受 API Key。
- `PUT /issues/{iid}/checklist` 是整组替换，少传的旧条目会被删掉。
- 列表参数 `q` 在内存里全量过滤，匹配标题、正文、编号和纯数字序号。
- `collab` 只嵌在 `GET /issues/{iid}` 的详情里，列表项没有。

## Web 的 TypeScript

前端类型生成是下一步，这单没做。要做的话得加一条 Node 侧生成链路，还要改掉几十个已经手写类型的组件。这单只把契约收成一份 yaml，并让 Go 类型和技能文档从它产出。

## 设计决策

### 单文件，不拆外部 `$ref`

路由和 schema 放在同一个 yaml 里。拆文件之后，codegen 和 apidocgen 都要解析跨文件引用，换来的只是单文件变短。文件内 `$ref` 够用。

### codegen 只生成类型

`oapi-codegen.yaml` 里 `generate.models` 为 true，不生成路由，也不生成 server interface。路由注册、中间件和字段级权限留在 `router.go` 与 handler。生成代码接管不了「JWT 与 API Key 都能进门，某个字段只有 JWT 能写」。

可选对象字段按配置保持切片而不是指针（`prefer-skip-optional-pointer-on-container-types`）。`date-time`、`uuid`、`email` 都映射成 `string`，和现在响应里的 RFC3339 字符串一致。

### 信封用 allOf

成功响应写成 `allOf: [Envelope, { properties.data: 具体类型 }]`。`{code, message, data}` 只定义一次，每个端点只收窄 `data`。oapi-codegen 因此把 `Envelope.Data` 生成成 `interface{}`，不会为每个端点再造一个响应包装类型。handler 继续用现有的 response 助手写信封。

### `x-go-type` 复用 model 枚举

`IssueState`、`IssueSource`、`IssueWorkflowStatus` 等枚举在 spec 里用 `x-go-type` 指回 `server/internal/model`。生成结果是别名，例如 `type IssueState = model.IssueState`。业务代码继续用 model 里的常量，不要在 `api.IssueState` 和 `model.IssueState` 之间来回转。

### 别名还是直接用生成类型

迁移已完成：handler 请求体直接绑定 `api.*JSONBody` / `api.*Request` 生成类型（或经 `service/api_types.go` 里的同名别名），service 响应直接构造 `api.*`。生成类型把可选字段做成指针，`api.Ptr`/`api.Deref`/`api.NonEmpty` 用来在边界处转换。

仍保留手写的例外：

- `service.UpdateDocumentRequest` 与 handler 的 `updateDocumentRequest`：`parent_id` 需要 省略/null/字符串 三态，生成类型的 `*string` 表达不了，handler 用 `json.RawMessage` 旁路捕获。
- handler 的 `updateMeInput`/`updateProjectInput`（对应 `api.UpdateProfileRequest`/`api.UpdateProjectRequest`）：字段指针化后 `binding:"omitempty"` 不再跳过显式空串，`{"username":""}` 会误触发校验。手写 `string + binding:"omitempty,..."` 输入结构绑定，再经 `api.NonEmpty` 折回 nil。
- `service.ShipResult`：内嵌 `api.ShipResult` 加一个 `json:"-"` 的内部字段。
- `ListLogEntriesRequest`/`ListLogRunsRequest`/`IssueListFilters`：query 参数过滤结构，不是 JSON 载荷。
- `minimaxChatRequest`/`minimaxChatResponse`：出向 MiniMax 调用的载荷，不属于本 API 契约。

新代码默认直接用 `api` 包的类型；仅当绑定/序列化语义与生成类型不等价时，按上面的例外模式手写并在注释里说明原因。

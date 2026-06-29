---
name: fast-ship
description: 通过 Fast Ship REST API 完成 Issue 的创建、更新与查询。支持 API Key 认证，适用于自动化场景和 Agent 集成。
---

# Fast Ship API

## 何时使用

当用户要求你：
- 在 Fast Ship 平台上创建或更新 Issue
- 查询项目、版本、Issue 列表或详情
- 通过程序化方式与 Fast Ship 交互（CI/CD、自动化脚本、Agent 操作）

## 认证配置

Fast Ship 使用 **API Key** 进行程序化认证。Key 格式为 `fsk_` 开头的随机字符串。

### 首次使用（配置持久化）

如果 `~/.config/fast-ship/config.yaml` 不存在，**必须**向用户询问以下信息：

1. **Base URL**：Fast Ship 服务地址，例如 `http://localhost:8080` 或 `https://fast-ship.example.com`
2. **API Key**：从 Fast Ship Web 界面「设置 → API Key」创建的密钥（`fsk_` 开头）

然后将配置写入文件：

```yaml
# ~/.config/fast-ship/config.yaml
base_url: "https://fast-ship.example.com"
api_key: "fsk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

> 注意：`~/.config` 目录在 macOS/Linux 上通常已存在；如果不存在，请先创建目录。

> **Breaking Change**：API Key **不再**可通过 `PUT /api/issues/:issue_id` 修改 `state` / `state_reason`（open/close）。Agent 应改用 `PUT /api/issues/:issue_id/internal-meta` 的 `workflow_status` 标记完成；Issue 的 open/close 由 JWT 用户在 Web 端操作。

### 后续使用

直接读取 `~/.config/fast-ship/config.yaml` 中的 `base_url` 和 `api_key`，**不再询问用户**。

### 请求头

所有 API 请求必须携带：

```
Authorization: Bearer <api_key>
Content-Type: application/json
```

## 核心工作流：创建/更新 Issue

> **默认来源规则**：当用户要求你创建 Issue 但未明确说明来源时，**默认创建 `source: "internal"` 的本地 Issue**。仅当用户明确说"同步到 GitHub"或"创建 GitHub Issue"时，才使用 `source: "github"`。

### 步骤 1：获取项目列表

列出用户可访问的项目，确定要操作的 `project_id`：

```http
GET /api/projects
```

响应示例：

```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "id": "proj-uuid",
        "name": "MyApp",
        "github_owner": "org",
        "github_repo": "myapp"
      }
    ]
  }
}
```

### 步骤 2：（可选）获取过滤选项

获取项目的标签、指派人、里程碑等，用于创建 Issue 时填充：

```http
GET /api/projects/:project_id/issues/filter-options
```

### 步骤 3：（可选）获取仓库标签

获取 GitHub 仓库标签列表：

```http
GET /api/projects/:project_id/issues/repo-labels
```

### 步骤 4：创建 Issue

```http
POST /api/projects/:project_id/issues
```

请求体（最简示例，未指定 `source` 时默认创建内部 Issue）：

```json
{
  "title": "问题标题",
  "body": "问题正文，支持 Markdown",
  "workflow_status": "todo"
}
```

显式指定 `source` 时：

- `source: "internal"` — 创建本地 Issue（不同步 GitHub）
- `source: "github"` — 创建并同步到关联的 GitHub 仓库

字段说明：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `title` | string | 是 | Issue 标题 |
| `body` | string | 否 | 正文内容，支持 Markdown |
| `workflow_status` | string | 否 | 内部 Issue 工作流状态：`todo` / `in_progress` / `done` |
| `source` | string | 否 | `internal` 或 `github`；**未指定时默认 `internal`** |

响应示例：

```json
{
  "code": 0,
  "data": {
    "id": "issue-uuid",
    "reference": "INT-1",
    "title": "问题标题",
    "source": "internal",
    "project_id": "proj-uuid",
    "internal_meta": {
      "workflow_status": "todo"
    }
  }
}
```

### 步骤 5：更新 Issue

```http
PUT /api/issues/:issue_id
```

请求体（JWT 用户示例，含 open/close）：

```json
{
  "title": "更新后的标题",
  "body": "更新后的正文",
  "state": "closed",
  "state_reason": "completed",
  "labels": ["bug", "priority-high"]
}
```

API Key 示例（仅允许 title/body/labels，**勿**携带 state/state_reason）：

```json
{
  "title": "更新后的标题",
  "body": "更新后的正文",
  "labels": ["bug"]
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `title` | string | 否 | 新标题 |
| `body` | string | 否 | 新正文 |
| `state` | string | 否 | `open` 或 `closed`；**仅 JWT**，API Key 传此字段返回 403（40301） |
| `state_reason` | string | 否 | 关闭原因；**仅 JWT**，API Key 传此字段返回 403（40301） |
| `labels` | string[] | 否 | 标签名称列表 |

### 修改工作流状态（internal-meta）

修改内部 Issue 的工作流状态（API Key 已支持，便于 Agent 推进状态机）：

```http
PUT /api/issues/:issue_id/internal-meta
```

```json
{
  "workflow_status": "in_progress"
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `workflow_status` | string | 是 | `todo` / `in_progress` / `done`（空串可重置为未开始） |

> Issue 评论（`POST /issues/:iid/comments`）、Checklist（`PUT /issues/:iid/checklist`）、附件上传（`POST /issues/:iid/assets`）也已对 API Key 开放，除 `state` / `state_reason` 外，请求体字段与网页端一致。

### 步骤 6：查询 Issue

```http
GET /api/issues/:issue_id
```

或列表查询（支持分页和过滤）：

```http
GET /api/projects/:project_id/issues?q=关键词&state=open&source=internal&workflow_status=todo&page=1&page_size=20
```

查询参数：

| 参数 | 说明 |
|---|---|
| `q` | 标题/正文关键词搜索 |
| `state` | `open` / `closed`（**查询过滤**，非写操作） |
| `source` | `internal` / `github` |
| `workflow_status` | `todo` / `in_progress` / `done` |
| `label` | 标签过滤 |
| `sort` | 排序，默认 `updated_desc` |
| `page` | 页码，默认 1 |
| `page_size` | 每页数量，默认 20，最大 100 |

## API Key 权限范围

当前 API Key 支持的操作：

| 资源 | 读 | 写 |
|---|---|---|
| 项目列表/详情 | ✅ | ❌ |
| Issue 列表/详情/过滤选项/标签 | ✅ | ✅（PUT 允许 title/body/labels，禁止 state/state_reason） |
| 版本列表/详情 | ✅ | ✅ |
| 构建产物上传/下载 | ✅ | ✅ |
| Issue 评论 | ✅ | ✅ |
| Issue 工作流状态更新（internal-meta） | ✅ | ✅ |
| Issue Checklist | ✅ | ✅ |
| Issue 附件上传 | ✅ | ✅ |
| 人机协作区（建议/计划/审查/总结） | ✅ | ✅ |
| Ship 发布 | ❌ | ❌ |
| AI 辅助功能 | ❌ | 部分 |

> Issue 的评论、工作流状态（internal-meta）、Checklist、附件上传、AI 清单建议等编辑类写操作已对 API Key 开放；但通用编辑 `PUT /issues/:iid` 中的 `state` / `state_reason` 仅限 JWT 用户，API Key 调用会返回 403（40301）。AI 端点中仅 `checklist-suggestions` 对 API Key 开放；`generate-title` 与 `/ai/settings` 仍限 JWT，API Key 调用会返回 `403`（40301）。

> 注意：人机协作区 JWT 用户可**读取与删除**，不可 PUT 写入；代理（API Key）可 PUT 写入与 DELETE 删除。
> - 代理（API Key）：写实施建议 / 计划 / 审查结果 / 完成总结（`PUT /suggestions`、`PUT /plan`、`PUT /review`、`PUT /summary`）；亦可 DELETE 清空或分块删除。
> - 用户（JWT）：可 `GET /collab` 只读浏览；可 DELETE 清空或分块删除；调用 PUT 写端点返回 `403`（40303）。
> - 读取（`GET /collab`）JWT 与 API Key 均可。

## 人机协作区（Issue Collaboration Area）

人机协作区挂在每个 Issue 之下：代理（API Key）产出全部内容（实施建议、计划、审查结果、完成总结），用户（网页登录 JWT）可只读浏览并删除不满意的内容，不可 PUT 写入。协作区数据保存在 Fast Ship 内部，不回写 GitHub；内部 Issue 与 GitHub Issue 均支持。

> **Breaking Change（相对旧版）**：旧的「背景 notes」「问题 questions」区块及端点（`POST/PUT/DELETE /collab/notes`、`POST/PUT/DELETE /collab/questions`）已**移除**；`GET /collab` 响应字段由 `notes/questions/summary` 改为 `suggestions/plan/review/summary`。已无「作答/补背景」概念。

### 接口总览

所有接口路径前缀 `/api/issues/:issue_id/collab`。`GET` 与 `DELETE` 两类凭证（JWT / API Key）均可；**PUT 写端点仅限 API Key**（代理），JWT 调用 PUT 返回 `403`（40303）。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/collab` | 获取整个协作区（建议/计划/审查/总结），JWT 或 API Key 均可 |
| DELETE | `/collab` | 一键清空协作区全部四块（JWT 或 API Key 均可，幂等 200） |
| PUT | `/collab/suggestions` | 全量替换实施建议列表（API Key） |
| DELETE | `/collab/suggestions` | 清空全部实施建议（JWT 或 API Key 均可，幂等 200） |
| PUT | `/collab/plan` | 写入/覆盖计划（API Key） |
| DELETE | `/collab/plan` | 删除计划（JWT 或 API Key 均可，幂等 200） |
| PUT | `/collab/review` | 写入/覆盖审查结果（API Key） |
| DELETE | `/collab/review` | 删除审查结果（JWT 或 API Key 均可，幂等 200） |
| PUT | `/collab/summary` | 写入/覆盖完成总结（API Key） |
| DELETE | `/collab/summary` | 删除完成总结（JWT 或 API Key 均可，幂等 200） |

> DELETE 幂等语义：Issue 存在且有访问权时，目标块不存在或协作区为空仍返回 200（与 artifact DELETE 对不存在资源返 404 不同）。对外清空建议请走 DELETE `/collab/suggestions`，勿用 PUT `items:[]`。清空后 GET 响应为 `null` / `[]`，与从未创建无法区分；Agent 重新 PUT 在技术上合法，但应先 GET 并尊重用户已清空的状态。

### GET 获取协作区

```http
GET /api/issues/:issue_id/collab
```

响应示例：

```json
{
  "code": 0,
  "data": {
    "suggestions": [
      {
        "id": "sug-uuid",
        "issue_id": "issue-uuid",
        "body": "在 router 增加 /collab/suggestions 路由",
        "sort_order": 0,
        "author": { "kind": "agent", "login": "代理" },
        "created_at": "2026-06-19T10:00:00Z",
        "updated_at": "2026-06-19T10:00:00Z"
      }
    ],
    "plan": {
      "issue_id": "issue-uuid",
      "body": "分两步：先加数据层，再加接口与前端",
      "author": { "kind": "agent", "login": "代理" },
      "created_at": "2026-06-19T11:00:00Z",
      "updated_at": "2026-06-19T11:00:00Z"
    },
    "review": null,
    "summary": null
  }
}
```

`suggestions` 未产出时为 `[]`；`plan`/`review`/`summary` 未产出时为 `null`。因仅 API Key 可写，`author.kind` 恒为 `agent`（`login` 固定为"代理"）。

### PUT 实施建议（API Key，全量替换）

```http
PUT /api/issues/:issue_id/collab/suggestions
```

请求体：

```json
{
  "items": [
    { "body": "建议新增顶部按钮" },
    { "body": "支持暗色模式" }
  ]
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `items` | array | 是 | 一次最多 30 条；`null` 返回 400，**空数组 `[]` 表示清空全部建议（后端兼容保留，对外请走 DELETE `/collab/suggestions`）** |
| `items[].body` | string | 是 | 单条建议正文，1..4000 字符，支持 Markdown |

每次 PUT 为**全量替换**：服务端先删除该 Issue 全部旧建议，再按 `items` 顺序批量创建（`sort_order` 由服务端按数组下标分配）。**建议 id 每次替换都会重建，不要缓存 id**；变更请 GET 后整包重新 PUT。

### PUT 计划（API Key，覆盖）

```http
PUT /api/issues/:issue_id/collab/plan
```

```json
{ "body": "详细执行计划，支持 Markdown 列表与代码块" }
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `body` | string | 是 | 计划正文，1..8000 字符，支持 Markdown |

每个 Issue 只有一份计划，重复 PUT 为覆盖更新（upsert，保留首次创建时间）。

### PUT 审查结果（API Key，覆盖）

```http
PUT /api/issues/:issue_id/collab/review
```

```json
{ "body": "## 结论\n通过。\n\n## 测试\n核心路径已覆盖。\n\n## 遗留\n无。" }
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `body` | string | 是 | 审查正文，1..8000 字符，支持 Markdown |

建议包含「结论 / 测试结果 / 遗留项」小节，便于人工扫读。每个 Issue 只有一份审查，重复 PUT 为覆盖更新（upsert）。

### PUT 完成总结（API Key，覆盖）

```http
PUT /api/issues/:issue_id/collab/summary
```

```json
{
  "body": "已新增顶部按钮，运营可在设置中开关。",
  "commit_ids": ["abc1234", "0123456789abcdef0123456789abcdef01234567"]
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `body` | string | 是 | 非技术性摘要，1..8000 字符，支持 Markdown |
| `commit_ids` | string[] | 否 | 提交 ID，0..20 个；**仅接受 git SHA（十六进制 7..64 位）**，不接受分支名/标签 |

每个 Issue 只有一条总结，重复 PUT 为覆盖更新（upsert）。注意：总结与审查结果职责不同——总结讲「做了什么」，审查结果讲「做得好不好」，两者并存。

### 推荐工作流（幂等）

外部代理无状态、易中断，建议按以下顺序产出：

1. **先读后写**：处理 Issue 前 `GET /collab`，确认是否已有建议/计划/审查/总结，避免重复覆盖；若用户已清空某块，尊重其意图勿直接重新生成。
2. **实施建议**：分析后 `PUT /collab/suggestions`（全量替换），列出该做的要点清单。
3. **计划**：`PUT /collab/plan`，给出落地执行计划（覆盖更新）。
4. **据计划实施**：执行代码改动（在用户许可范围内）。
5. **审查结果**：实施后 `PUT /collab/review`，写质量审查报告（结论/测试/遗留）。
6. **完成总结**：`PUT /collab/summary`，附非技术摘要与提交 SHA。
7. 重申规则：未经用户许可**不要**修改 Issue 的 open/closed 状态，也**不要**提交/推送代码。

## 项目日志（INT-49）

受管项目可通过 API Key 批量上传结构化日志；JWT 用户与 API Key 均可读取与删除。**上传不去重**，网络重试可能导致重复条目，调用方应自行避免重发。

### 上传日志（仅 API Key）

```http
POST /api/projects/:project_id/logs
```

```json
{
  "run_id": "2f086286-1112-45dd-a8fa-824df6d5949c",
  "source": "smux",
  "entries": [
    {
      "timestamp": "2026-06-29T14:30:00Z",
      "level": "info",
      "source": "phase-1",
      "message": "阶段完成",
      "metadata": { "phase": 1 }
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `run_id` | 必填，1..128 字符，`^[A-Za-z0-9_-]+$`；同项目同 run_id 多次上传合并到同一批次 |
| `source` | 可选批次来源标签 |
| `entries` | 必填，1..500 条/次；body 总大小 ≤ 4 MB（超出返回 HTTP 413） |
| `entries[].level` | `debug` / `info` / `warn` / `error` / `fatal`，非法值整批拒绝 |
| `entries[].message` | 1..4000 字节 |
| `entries[].metadata` | 可选 JSON，序列化后 ≤ 4 KB |

JWT 调用上传返回 `403`（40303）。

### 查询日志条目（JWT + API Key）

```http
GET /api/projects/:project_id/logs
```

查询参数：`batch_id`、`run_id`、`level`、`entry_source`、`batch_source`、`q`（message 关键词）、`from`/`to`（ISO 8601）、`page`/`page_size`（默认 1/50，最大 100）、`sort`（默认 `timestamp_desc`）。

### 查询批次列表（JWT + API Key）

```http
GET /api/projects/:project_id/log-batches
```

查询参数：`run_id`、`batch_source`、`from`/`to`（按 last_entry_at）、`page`/`page_size`。

### 删除（JWT + API Key）

```http
DELETE /api/log-batches/:batch_id
DELETE /api/projects/:project_id/logs
```

前者删除整批（级联删条目）；后者清空该项目全部日志。

## 错误处理

常见错误码：

| HTTP 状态 | Code | 含义 |
|---|---|---|
| 400 | 40001 | 请求参数无效（超长、`items` 为 null、commit_id 非 SHA 等） |
| 401 | 40100 | 认证信息无效或缺失 |
| 403 | 40300 | API Key 权限不足（尝试访问未开放的写操作） |
| 403 | 40303 | 该操作仅限 API Key 调用（协作区 **PUT** 写端点拒绝 JWT） |
| 404 | 40401 | 项目不存在 |
| 404 | 40405 | Issue 不存在 |
| 404 | 40408 | 协作区内容不存在（预留错误码，当前 GET/DELETE 端点均不产生） |
| 404 | 40409 | 日志批次不存在 |
| 413 | — | 请求体超出 4 MB（日志上传） |

如果收到 401，检查：
1. `Authorization` 头是否正确携带 `Bearer fsk_...`
2. `base_url` 是否指向正确的 Fast Ship 实例
3. API Key 是否已被删除

## 请求编码

所有请求统一使用 **UTF-8 without BOM** 编码，请求头必须声明：

```
Content-Type: application/json; charset=utf-8
```

建议将 JSON 请求体写入临时文件后发送，避免 shell/终端编码转换导致服务端保存乱码（Windows CMD/PowerShell 下尤为常见）：

```bash
curl -X POST "$BASE_URL/api/projects/$PROJECT_ID/issues" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json; charset=utf-8" \
  --data-binary @request.json
```

> 避免在命令行参数中直接拼接 `curl -d '{"title":"中文"}' ...`。

## 注意事项

- Issue 创建后，`reference` 字段是短编号（如 `INT-1`），`id` 是 UUID。更新操作使用 `id`。
- `source: "github"` 的 Issue 会同步创建到关联的 GitHub 仓库，需要项目已配置 GitHub Token。
- `workflow_status` 仅对 `source: "internal"` 有效。
- 所有时间字段均为 ISO 8601 格式。

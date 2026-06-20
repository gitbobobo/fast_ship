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

请求体：

```json
{
  "title": "更新后的标题",
  "body": "更新后的正文",
  "state": "closed",
  "state_reason": "completed",
  "labels": ["bug", "priority-high"]
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `title` | string | 否 | 新标题 |
| `body` | string | 否 | 新正文 |
| `state` | string | 否 | `open` 或 `closed` |
| `state_reason` | string | 否 | 关闭原因 |
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

> Issue 评论（`POST /issues/:iid/comments`）、Checklist（`PUT /issues/:iid/checklist`）、附件上传（`POST /issues/:iid/assets`）也已对 API Key 开放，请求体字段与网页端一致。

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
| `state` | `open` / `closed` |
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
| Issue 列表/详情/过滤选项/标签 | ✅ | ✅ |
| 版本列表/详情 | ✅ | ✅ |
| 构建产物上传/下载 | ✅ | ✅ |
| Issue 评论 | ✅ | ✅ |
| Issue 工作流状态更新（internal-meta） | ✅ | ✅ |
| Issue Checklist | ✅ | ✅ |
| Issue 附件上传 | ✅ | ✅ |
| 人机协作区（背景/问题/总结） | ✅ | 部分 |
| Ship 发布 | ❌ | ❌ |
| AI 辅助功能 | ❌ | 部分 |

> Issue 的编辑类写操作（评论、工作流状态、Checklist、附件上传、AI 清单建议）均已对 API Key 开放，与通用编辑 `PUT /issues/:iid` 行为一致，便于自动化/Agent 场景。AI 端点中仅 `checklist-suggestions` 对 API Key 开放；`generate-title` 与 `/ai/settings` 仍限 JWT，API Key 调用会返回 `403`（40301）。

> 注意：人机协作区按角色分工做了**写权限切分**——
> - 代理（API Key）：可创建/删除问题、写完成总结（`POST /questions`、`DELETE /questions/:id`、`PUT /summary`）。
> - 用户（网页登录 JWT）：作答、补充/编辑/删除背景信息（`PUT /questions/:id/answer`、`notes` 增删改）。
> - 即：API Key 调用作答或背景接口会返回 `403`（40301）；JWT 调用提问/总结接口仍允许（无破坏性）。
> - 读取（`GET /collab`）JWT 与 API Key 均可。

## 人机协作区（Issue Collaboration Area）

人机协作区挂在每个 Issue 之下，用于"人（用户）↔ 代理"的结构化协作：代理提出带选项的澄清问题、用户作答、代理完成后写非技术摘要 + 提交 ID 供人工审核。协作区数据保存在 Fast Ship 内部，不回写 GitHub；内部 Issue 与 GitHub Issue 均支持。

### 接口总览

所有接口路径前缀 `/api/issues/:issue_id/collab`，均需 `Authorization: Bearer fsk_...`。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/collab` | 获取整个协作区（背景/问题/总结） |
| POST | `/collab/notes` | 补充一条背景信息（用户） |
| PUT | `/collab/notes/:note_id` | 编辑背景信息 |
| DELETE | `/collab/notes/:note_id` | 删除背景信息 |
| POST | `/collab/questions` | 批量创建问题（代理，可带选项） |
| PUT | `/collab/questions/:question_id/answer` | 作答/改答（用户） |
| DELETE | `/collab/questions/:question_id` | 删除问题（代理） |
| PUT | `/collab/summary` | 写入/覆盖完成总结（代理） |

### GET 获取协作区

```http
GET /api/issues/:issue_id/collab
```

响应示例：

```json
{
  "code": 0,
  "data": {
    "notes": [
      {
        "id": "note-uuid",
        "issue_id": "issue-uuid",
        "body": "这个按钮主要给运营用",
        "author": { "kind": "user", "login": "alice", "avatar_url": "/api/avatars/..." },
        "created_at": "2026-06-19T10:00:00Z",
        "updated_at": "2026-06-19T10:00:00Z"
      }
    ],
    "questions": [
      {
        "id": "q-uuid",
        "issue_id": "issue-uuid",
        "body": "按钮放哪里？",
        "options": ["顶部", "侧边"],
        "sort_order": 0,
        "author": { "kind": "agent", "login": "代理" },
        "answer": null,
        "created_at": "2026-06-19T10:00:00Z",
        "updated_at": "2026-06-19T10:00:00Z"
      }
    ],
    "summary": null
  }
}
```

`author.kind` 为 `user` 或 `agent`：API Key 写入的内容显示为 `agent`（`login` 固定为"代理"），用户写入的显示为 `user`（`login` 为用户名）。`questions[].answer` 为 `null` 表示尚未作答；作答后为 `{ "value": "...", "author": {...}, "answered_at": "..." }`。

### POST 批量创建问题（代理）

```http
POST /api/issues/:issue_id/collab/questions
```

请求体：

```json
{
  "items": [
    { "body": "按钮放哪里？", "options": ["顶部", "侧边"] },
    { "body": "还需要支持什么？", "options": [] }
  ]
}
```

字段与限制：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `items` | array | 是 | 一次最多 20 条 |
| `items[].body` | string | 是 | 问题正文，1..1000 字符（按字符计） |
| `items[].options` | string[] | 否 | 选项，0..8 个，每个 1..100 字符；为空表示纯自由文本问题 |

`sort_order` 由服务端按创建顺序自动分配。问题正文与选项**创建后不可修改**，只能删除后重建。

### PUT 作答（用户）

```http
PUT /api/issues/:issue_id/collab/questions/:question_id/answer
```

```json
{ "answer": "顶部" }
```

`answer` 为单一值：可直接填某个选项原文，也可填自由文本（1..1000 字符）。重复调用为**改答**，覆盖旧值。

### PUT 写完成总结（代理）

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

每个 Issue 只有一条总结，重复 PUT 为覆盖更新（upsert）。

### 背景信息（notes）

```http
POST /api/issues/:issue_id/collab/notes        # { "body": "..." }
PUT /api/issues/:issue_id/collab/notes/:note_id # { "body": "..." }
DELETE /api/issues/:issue_id/collab/notes/:note_id
```

`body` 为 1..4000 字符的纯文本，由用户主动补充供代理参考。

### 推荐工作流（幂等）

外部代理无状态、易中断，建议按以下流程，避免重复创建：

1. **先读后写**：处理 Issue 前 `GET /collab`，确认是否已有未回答的问题或已有总结，避免重复创建。
2. **创建问题**：用 `POST /collab/questions` 一次提出本批需要澄清的非技术问题；本地记录返回的 `question_id`。
3. **轮询作答**：周期性 `GET /collab`，检查 `questions[].answer` 是否齐全（已由用户作答）。
4. **据答处理**：所有问题作答后再继续；**不要删除已作答的问题**（会让轮询误判用户未答）。
5. **写总结**：完成后 `PUT /collab/summary`（upsert），附非技术摘要与提交 SHA。
6. 重申规则：未经用户许可**不要**修改 Issue 的 open/closed 状态，也**不要**提交/推送代码。

## 错误处理

常见错误码：

| HTTP 状态 | Code | 含义 |
|---|---|---|
| 400 | 40001 | 请求参数无效（超长、选项过多、commit_id 非 SHA 等） |
| 401 | 40100 | 认证信息无效或缺失 |
| 403 | 40300 | API Key 权限不足（尝试访问未开放的写操作） |
| 404 | 40401 | 项目不存在 |
| 404 | 40405 | Issue 不存在 |
| 404 | 40408 | 协作区内容（note/question）不存在 |

如果收到 401，检查：
1. `Authorization` 头是否正确携带 `Bearer fsk_...`
2. `base_url` 是否指向正确的 Fast Ship 实例
3. API Key 是否已被删除

## 注意事项

- Issue 创建后，`reference` 字段是短编号（如 `INT-1`），`id` 是 UUID。更新操作使用 `id`。
- `source: "github"` 的 Issue 会同步创建到关联的 GitHub 仓库，需要项目已配置 GitHub Token。
- `workflow_status` 仅对 `source: "internal"` 有效。
- 所有时间字段均为 ISO 8601 格式。

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

### Breaking Changes

- API Key **不可**通过 `PUT /api/issues/:issue_id` 修改 `state` / `state_reason`（open/close）；Agent 应改用 `PUT /api/issues/:issue_id/internal-meta` 的 `workflow_status` 标记完成，open/close 由 JWT 用户在 Web 端操作。
- API Key **不可**发表评论（`POST /api/issues/:issue_id/comments`）。
- API Key **不可** `PUT`/`DELETE /api/issues/:issue_id/ship-hook`。`ship_hook` 仅出现在 Issue GET/列表中供只读。发货后关单/留言由 JWT 用户在 Web 配置，发货成功后由服务器执行。
- 协作区旧端点 `POST/PUT/DELETE /collab/notes`、`/collab/questions` 已移除；`suggestions`、`plan`、`review` 与 `commit_ids` 一并废弃。当前 `GET /collab` 只返回 `consensus` 与 `summary`。

### 后续使用

直接读取 `~/.config/fast-ship/config.yaml` 中的 `base_url` 和 `api_key`，**不再询问用户**。

### 请求头

所有 API 请求必须携带：

```
Authorization: Bearer <api_key>
Content-Type: application/json
```

## 核心工作流：创建/更新 Issue

> **默认来源**：未说明时创建 `source: "internal"` 本地 Issue；仅当用户明确要求同步 GitHub 时才用 `source: "github"`（API Key 仅可 `internal`）。

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

### 步骤 2：打标前获取标签列表

```http
GET /api/projects/:project_id/issues/repo-labels
```

若项目未配置 GitHub 仓库或标签为空，改用：

```http
GET /api/projects/:project_id/issues/filter-options
```

### 步骤 3：创建 Issue 并打标

**3a. 创建** — `POST /api/projects/:project_id/issues`

```json
{
  "title": "问题标题",
  "body": "问题正文，支持 Markdown"
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `title` | string | 是 | Issue 标题 |
| `body` | string | 否 | 正文，支持 Markdown |
| `workflow_status` | string | 否 | `todo` / `in_progress` / `done`；建议不传，保持未设置 |
| `source` | string | 否 | `internal`（默认）或 `github` |

创建响应中的 `id`（UUID）用于后续更新；`reference` 为短编号（如 `INT-1`）。用户侧网页地址为 `{base_url}/projects/{project_id}/issues/{id}`（Web 前端与 API 同源，路由里的 `iid` 也只认 UUID）。

**3b. 打标** — `PUT /api/issues/:issue_id`

根据步骤 2 的标签列表，结合标题/正文语义选取**已存在**的标签名；若无合适标签可跳过。

```json
{
  "labels": ["bug", "priority-high"]
}
```

> 需要推进工作流时，调用 `PUT /api/issues/:issue_id/internal-meta`（`workflow_status`: `todo` / `in_progress` / `done`），勿依赖创建时设置 `workflow_status`。

### 步骤 4：后续更新 Issue

`PUT /api/issues/:issue_id`

API Key 示例（仅 `title` / `body` / `labels`，**勿**携带 `state` / `state_reason`）：

```json
{
  "title": "更新后的标题",
  "body": "更新后的正文",
  "labels": ["bug"]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `title` / `body` / `labels` | — | API Key 可写 |
| `state` / `state_reason` | — | **仅 JWT**；API Key 传入返回 403（40301） |

JWT 用户另可传 `state: "closed"` 与 `state_reason: "completed"` 关闭 Issue。

工作流状态单独更新：

```http
PUT /api/issues/:issue_id/internal-meta
```

```json
{ "workflow_status": "in_progress" }
```

`workflow_status` 必填；空串可重置为未开始。另可对 API Key 开放：`PUT /issues/:iid/checklist`、`POST /issues/:iid/assets`。

### 步骤 5：查询 Issue

```http
GET /api/issues/:issue_id
```

列表查询（分页与过滤）：

```http
GET /api/projects/:project_id/issues?q=关键词&state=open&source=internal&workflow_status=todo&page=1&page_size=20
```

| 参数 | 说明 |
|---|---|
| `q` | 标题/正文关键词 |
| `state` | `open` / `closed`（**查询过滤**，非写操作） |
| `source` | `internal` / `github` |
| `workflow_status` | `todo` / `in_progress` / `done` |
| `label` | 标签过滤 |
| `sort` | 默认 `updated_desc` |
| `page` / `page_size` | 默认 1 / 20，最大 100 |

### 发货后钩子（`ship_hook`，只读）

`GET /api/issues/:issue_id` 及列表项可能带 `ship_hook`（无钩子时字段省略）。`PUT`/`DELETE /api/issues/:issue_id/ship-hook` **仅 JWT**；API Key 调用返回 403（40301）。**不要**用 API Key 发评论或改 `state` 来「代替」钩子；Agent 继续只用 `internal-meta.workflow_status` 标记进度。创建 Issue 时**不要**带钩子，**不要**设钩子。

发货后钩子是 JWT 用户在 Web 上配置的一次性动作：该 Issue 所属项目下一次任意版本 **成功** 发货后执行。可选动作：发一条顶层评论、关闭问题（`state_reason=completed`）、改内部状态。

`pending` 示例（`comment_enabled` / `close_enabled` / `workflow_enabled` 为显式布尔，**总是出现**，false 也输出；`workflow_status` 同样总是出现，`workflow_enabled=true` 且值为 `""` 表示「重置为未设置」；`comment_body` 仅在启用评论动作时出现）：

```json
{
  "status": "pending",
  "comment_enabled": true,
  "comment_body": "已随 {version} 发出。",
  "close_enabled": true,
  "workflow_enabled": true,
  "workflow_status": "done"
}
```

`fired` 另有 `version_id`、`version_number`、`release_url`、`fired_at`、`results`（每步 `ok` / `skipped` / `error`）。占位符 `{version}`、`{release_url}` 在发货时替换，`comment_body` 随之变为渲染后正文。Agent 看到 `pending` 只表示用户已预约，**不要**自行再关单或留言。

`running` 表示服务器正在执行该预约；Agent 不应干预或代执行。`fired` 且带内部 `retry_pending` 表示上次动作未持久化完成，服务器会在后续成功发货时自动重试，仍归属于原触发版本；Agent 不应自行补执行。

## API Key 权限范围

| 资源 | 读 | 写 |
|---|---|---|
| 项目列表/详情 | ✅ | ❌ |
| Issue 列表/详情/过滤选项/标签 | ✅ | ✅（PUT：`title` / `body` / `labels`） |
| 版本列表/详情 | ✅ | ✅ |
| 构建产物上传/下载 | ✅ | ✅ |
| Issue 评论 | ✅ | ❌ |
| Issue 工作流（internal-meta）/ Checklist / 附件 | ✅ | ✅ |
| Issue 发货后钩子（ship-hook） | ✅（随 Issue GET/列表只读） | ❌ |
| 人机协作区 | ✅ | ✅（PUT 写；JWT 只读 + DELETE） |
| Issue 推荐 | ✅ | ✅（PUT 仅 API Key；JWT 可 GET + DELETE） |
| Ship 发布 | ❌ | ❌ |
| AI 辅助 | ❌ | 部分（仅 `checklist-suggestions`） |

> **凭证分工**：仅 JWT — `state` / `state_reason`、发表评论、`ship-hook` 写入、`generate-title`、`/ai/settings`；仅 API Key — 协作区 PUT、推荐 PUT、日志上传（AI 端点中仅 `checklist-suggestions` 对 API Key 开放）。越权返回 403（40301 或 40303）。

## 人机协作区

代理（API Key）产出**共识**与**完成总结**两块；用户（JWT）可只读浏览并 DELETE，不可 PUT。数据存于 Fast Ship 内部，不回写 GitHub。

路径前缀 `/api/issues/:issue_id/collab`。**GET / DELETE**：JWT 或 API Key；**PUT 写端点**：仅 API Key（JWT 返回 40303）。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/collab` | 获取协作区全部内容（`consensus` 与 `summary`） |
| DELETE | `/collab` | 清空两块（幂等 200） |
| PUT | `/collab/consensus` | 写入/覆盖共识（API Key） |
| DELETE | `/collab/consensus` | 删除共识（幂等） |
| PUT | `/collab/summary` | 写入/覆盖完成总结（API Key） |
| DELETE | `/collab/summary` | 删除完成总结（幂等） |

> DELETE 幂等：目标块不存在仍返回 200。Agent 重新 PUT 前应先 GET，尊重用户已清空的状态。

### GET 响应示例

```json
{
  "code": 0,
  "data": {
    "consensus": {
      "issue_id": "…",
      "body": "…",
      "author": { "kind": "agent", "login": "代理" },
      "created_at": "…",
      "updated_at": "…"
    },
    "summary": null
  }
}
```

`consensus` / `summary` 未产出为 `null`。

### PUT 共识（覆盖 upsert）

```http
PUT /api/issues/:issue_id/collab/consensus
```

```json
{ "body": "…" }
```

`body` 1..8000 字符，Markdown。每 Issue 一份，重复 PUT 覆盖。

### PUT 完成总结（覆盖 upsert）

```http
PUT /api/issues/:issue_id/collab/summary
```

```json
{ "body": "…" }
```

`body` 1..8000 字符，Markdown，覆盖 upsert。`commit_ids` 字段已移除：PUT summary 只收 `body`，多传 `commit_ids` 会被忽略，不要依赖它。

### Summary 内容约束

给普通人看，短、能读完。结构是「**之前**怎样 / **现在**怎样」两段。三条禁令：不准列文件，不准贴 commit SHA，不准写测试清单。可感知的改动落地后再写「现在」；半成品不要写。

### 推荐工作流

1. `GET /collab` — 先读后写，尊重用户已清空的内容
2. 追问确认决策（用户明确表示别追问则跳过）
3. 整棵决策树问完、用户确认可以开工后，才 `PUT /collab/consensus` 写**已拍板的决策**。追问过程中不写。用户明确说「别问了直接做」→ **共识留空**，不要编「按 Issue 原文实施」充数
4. 实施（用户许可范围内）
5. `PUT /collab/summary` — 之前/现在两段；禁文件列表、禁 SHA、禁测试清单
6. 未经用户许可**不要**改 Issue open/closed 状态，**不要**提交/推送代码

用户 DELETE 清空后，本轮**不要**自动写回，除非用户又一起拍了新板；共识允许反复 PUT 覆盖，不留历史。

## 推荐任务

Agent（API Key）把某个 Issue 标记为「推荐做」，附推荐理由与前置依赖 Issue；用户在 Web 看板「推荐」弹框中查看并可移除。推荐会随 Issue 状态自动消失：`workflow_status` 进入 `in_progress` / `done`，或 `state` 变为 `closed`（含 GitHub 同步关闭）时，该推荐被硬删，不恢复。

**PUT 写入**：仅 API Key（JWT 返回 40303）。**GET / DELETE**：JWT 或 API Key 均可。

| 方法 | 路径 | 用途 |
|---|---|---|
| PUT | `/api/issues/:issue_id/recommendation` | 写入/覆盖推荐（API Key） |
| DELETE | `/api/issues/:issue_id/recommendation` | 移除推荐（不存在返回 40411） |
| GET | `/api/recommendations?project_id=` | 推荐列表；`project_id` 为空返回当前用户全部项目 |

### PUT 写入（覆盖 upsert）

```http
PUT /api/issues/:issue_id/recommendation
```

```json
{
  "reason": "阻塞了两个后续任务，建议优先做",
  "priority": "high",
  "dependencies": ["dep-issue-uuid-1", "dep-issue-uuid-2"]
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `reason` | string | 是 | 推荐理由，1–500 字符（空白会 trim 后判空） |
| `priority` | string | 否 | `high` / `medium` / `low`，缺省 `medium` |
| `dependencies` | string[] | 否 | 前置依赖的 Issue UUID 数组，≤20 个，不含自身；重复 PUT 会整组替换 |

Upsert 语义：同一 Issue 重复 PUT 整体覆盖 `reason` / `priority` / `dependencies` / `created_by`，保留原 `created_at`。`created_by` 由服务端记录为 API Key 名称，不需要传。

校验：目标 Issue 不存在返回 404（40405）；依赖 Issue 不存在或属于其他用户的项目同样 40405（不区分两者）；同一账号下跨项目依赖允许。目标 Issue 须 `state=open` 且 `workflow_status` 为未设置或 `todo`，否则返回 409（40910）——已被推荐过的 Issue 开始开发后无法再写推荐。

### GET 响应示例

```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "issue": {
          "id": "issue-uuid",
          "project_id": "proj-uuid",
          "project_name": "MyApp",
          "source": "internal",
          "sequence_number": 12,
          "title": "登录接口限流",
          "state": "open",
          "workflow_status": "todo"
        },
        "reason": "阻塞了两个后续任务，建议优先做",
        "priority": "high",
        "created_by": "CI-Rec",
        "created_at": "2026-10-04T08:00:00Z",
        "updated_at": "2026-10-04T08:00:00Z",
        "dependencies": [
          {
            "issue_id": "dep-uuid",
            "title": "接入限流组件",
            "state": "closed",
            "workflow_status": "done",
            "project_id": "proj-uuid",
            "sequence_number": 7
          }
        ]
      }
    ]
  }
}
```

排序：`priority` 降序（high > medium > low）后按 `updated_at` 降序。一次全量返回，无分页。

### DELETE 移除

```http
DELETE /api/issues/:issue_id/recommendation
```

推荐不存在返回 404（40411）。用户在 Web 端移除后，如需继续推荐必须重新 PUT（目标 Issue 此时须仍满足可推荐状态）。

## 项目日志

API Key 上传；JWT / API Key 均可查询与删除。对外只认一次运行（run），没有批次。`run_id` 由客户端生成，项目内唯一。分片用必填 `chunk_id` 做幂等；用 `chunk_id` 挡重试，**不要**复用 `chunk_id` 传不同内容。

Web：`/logs` 运行列表；`/logs/:runId` 按 `run_id` 看条目（URL 需带 project）。

### 上传（仅 API Key）

```http
POST /api/projects/:project_id/logs
```

```json
{
  "run_id": "2f086286-1112-45dd-a8fa-824df6d5949c",
  "chunk_id": "0",
  "source": "smux",
  "description": "可选运行说明",
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
| `run_id` | 必填，1..128 字符，`^[A-Za-z0-9_-]+$`；同项目同 run_id 合并为一次运行 |
| `chunk_id` | 必填，规则同 run_id；同一项目 `(run_id, chunk_id)` 重复提交返回 200，`duplicate: true`，`accepted_count: 0`，不插入 |
| `description` / `source` | 可选；**仅创建这次运行时写入**（先到先得） |
| `entries` | 必填，1..500 条/次；body 总大小 ≤ 4 MB（超出 HTTP 413）；一次运行最多 50,000 条，超出该分片整包拒绝（40909） |
| `entries[].level` | `debug` / `info` / `warn` / `error` / `fatal` |
| `entries[].message` | 1..4000 字节 |
| `entries[].metadata` | 可选 JSON，≤ 4 KB |

上传响应主键为 `run_id`，含 `accepted_count`、`duplicate`；没有批次 `id`。

### 查询与删除

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/projects/:project_id/logs` | 条目；参数：`run_id`、`level`、`entry_source`、`q`、`from`/`to`、`page`/`page_size`（默认 50，最大 100）、`sort` |
| DELETE | `/api/projects/:project_id/logs` | 清空项目全部日志 |
| GET | `/api/projects/:project_id/log-runs` | 运行列表；参数：`run_id`、`source`、`from`/`to` |
| GET | `/api/projects/:project_id/log-runs/:run_id` | 单次运行；不存在 404（40409） |
| DELETE | `/api/projects/:project_id/log-runs/:run_id` | 删除该次运行 |

## 错误处理

| HTTP | Code | 含义 |
|---|---|---|
| 400 | 40001 | 请求参数无效 |
| 401 | 40100 | 认证信息无效或缺失 |
| 403 | 40301 | API Key 无此操作权限 |
| 403 | 40303 | 该操作仅限 API Key（如协作区 PUT 拒绝 JWT） |
| 404 | 40401 | 项目不存在 |
| 404 | 40405 | Issue 不存在 |
| 404 | 40409 | 日志运行不存在 |
| 404 | 40411 | 推荐不存在 |
| 409 | 40909 | 该运行日志条数已达上限 |
| 409 | 40910 | 该 issue 当前状态不可被推荐 |
| 413 | — | 请求体超出 4 MB |

收到 401 时检查：`Authorization: Bearer fsk_...`、`base_url`、Key 是否已删除。

## 请求编码

使用 **UTF-8**，请求头声明 `Content-Type: application/json; charset=utf-8`。建议 `--data-binary @request.json`，避免 shell 拼接中文乱码。

## 注意事项

- 更新 Issue 使用 `id`（UUID），非 `reference`。
- `workflow_status` 仅对 `source: "internal"` 有效；所有时间为 ISO 8601。

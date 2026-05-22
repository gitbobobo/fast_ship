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
| Issue 评论 | ✅ | ❌ |
| Issue 工作流状态更新 | ✅ | ❌ |
| Issue Checklist | ✅ | ❌ |
| Issue 附件上传 | ✅ | ❌ |
| Ship 发布 | ❌ | ❌ |
| AI 辅助功能 | ❌ | ❌ |

## 错误处理

常见错误码：

| HTTP 状态 | Code | 含义 |
|---|---|---|
| 401 | 40100 | 认证信息无效或缺失 |
| 403 | 40300 | API Key 权限不足（尝试访问未开放的写操作） |
| 404 | 40400 | 项目或 Issue 不存在 |
| 400 | 40000 | 请求参数错误 |

如果收到 401，检查：
1. `Authorization` 头是否正确携带 `Bearer fsk_...`
2. `base_url` 是否指向正确的 Fast Ship 实例
3. API Key 是否已被删除

## 注意事项

- Issue 创建后，`reference` 字段是短编号（如 `INT-1`），`id` 是 UUID。更新操作使用 `id`。
- `source: "github"` 的 Issue 会同步创建到关联的 GitHub 仓库，需要项目已配置 GitHub Token。
- `workflow_status` 仅对 `source: "internal"` 有效。
- 所有时间字段均为 ISO 8601 格式。

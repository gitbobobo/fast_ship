---
name: fast-ship
description: 通过 Fast Ship REST API 完成 Issue 的创建、更新与查询。支持 API Key 认证，适用于自动化场景和 Agent 集成。请求体、查询参数和错误码以 references/api.md 为准。
---

# Fast Ship API

| 想做什么 | 实际调用 |
|---|---|
| 在 issue 上留言/发评论/写完成总结 | `PUT /api/issues/:iid/collab/summary`（API Key 发评论必 40301） |
| 标记完成 | `PUT /api/issues/:iid/internal-meta` 置 `workflow_status=done`（不是 `state`） |
| 关闭 issue | 不可——仅 JWT，API Key 传 `state` 必 40301 |
| 开始做了 | `PUT /api/issues/:iid/internal-meta` 置 `in_progress` |
| 记录共识/决策 | `PUT /api/issues/:iid/collab/consensus` |
| 按 `INT-xxx` 找 issue | `GET /api/projects/:pid/issues?q=INT-56` 换 UUID（路径参数只认 UUID） |

**调用任何写接口前先读 `references/api.md` 对应小节，不要凭记忆构造请求体。**

上表的 `:pid` 在 api.md 里是 `{id}`，`:iid` 是 `{iid}`。

## 认证配置

Fast Ship 使用 **API Key** 进行程序化认证。Key 格式为 `fsk_` 开头的随机字符串。

### 请求方式（硬性规则）

- **禁止**把 API Key 读出来写进命令、参数、环境变量赋值或任何输出——`curl -H "Authorization: Bearer fsk_..."` 这类写法会把 Key 留在会话记录和进程列表里。
- 所有请求一律走技能自带脚本。脚本自行读取 config 并附加认证头，Key 不出现在命令行：

```bash
node <技能目录>/scripts/fast-ship-api.mjs <METHOD> <path> [body.json] [--verify [读回路径]]
```

- 需要请求体时，把 JSON 写成 UTF-8 文件，以 `body.json` 位置参数传入。
- `--verify [读回路径]`：写请求成功（2xx）后再发一次 GET，输出 `{ "response": …, "verify": … }`，用于写后读回校验。缺省对同一路径 GET；没有对应 GET 路由的写端点（如 `internal-meta`、`recommendation`、`collab` 的 PUT；批量 `PUT /api/issues/internal-meta` 的缺省读回会命中 `GET /issues/:iid` 而 404）必须显式给出读回路径（如对 Issue 本身 `GET /api/issues/:iid`）。对 DELETE，读回 404 视为确认删除。
- 脚本不可用时的兜底：把 `Authorization: Bearer <api_key>` 整行写入临时头文件，用 `curl -H @<header文件>` 引用，用完**立即删除**该文件。

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

脚本会自行读取 `~/.config/fast-ship/config.yaml` 中的 `base_url` 和 `api_key`，**不再询问用户**。Agent **不要**把 Key 读出来写进命令或输出。

### 请求头

脚本自动为所有请求携带以下请求头（无需手工添加）：

```
Authorization: Bearer <api_key>
Content-Type: application/json; charset=utf-8
```

## 核心工作流

未说明来源时创建 `source: "internal"`。只有用户明确要求同步 GitHub 时才用 `source: "github"`。API Key 只能建 internal。

字段一律见 `references/api.md`，这里只留端点。

1. `GET /api/projects`，确定 `project_id`。
2. 打标前先 `GET /api/projects/{id}/issues/repo-labels`。项目没配 GitHub 仓库或标签为空时，改 `GET /api/projects/{id}/issues/filter-options`。
3. `POST /api/projects/{id}/issues` 创建。后续路径只用响应里的 `id`（UUID）。给用户的页面是 `{base_url}/projects/{project_id}/issues/{id}`。
4. 要打标就 `PUT /api/issues/{iid}`，标签名必须已经存在。不要靠创建请求里的 `workflow_status` 推进进度。
5. 推进用 `PUT /api/issues/{iid}/internal-meta`，值为 `todo`、`in_progress` 或 `done`。
6. 查一条用 `GET /api/issues/{iid}`。列表用 `GET /api/projects/{id}/issues`。

## 语义陷阱与权限分工

### multipart

这些端点是 `curl -F file=@路径`，不是 JSON。Issue 图片的响应里有 `markdown`，把这段贴进正文才算挂上附件。

- `POST /api/auth/avatar`
- `POST /api/projects/{id}/issues/assets`（草稿，仅 JWT）
- `POST /api/issues/{iid}/assets`
- `POST /api/versions/{vid}/artifacts`（可选表单字段 `platform`）

### 字段级权限

spec 的 security 经常同时写 JWT 和 API Key，真正的拒绝在 handler 里。

- API Key 不能传 `state` / `state_reason`，不能 `POST /api/issues/{iid}/comments`，不能 `PUT` 或 `DELETE /api/issues/{iid}/ship-hook`。做了就是 403（40301）。`ship_hook` 只出现在 Issue 的 GET 和列表里，只读。
- `PUT /api/issues/{iid}/collab/consensus`、`PUT /api/issues/{iid}/collab/summary`、`PUT /api/issues/{iid}/recommendation` 只接受 API Key。JWT 调用返回 403（40303）。
- 推荐被用户延后后，`PUT recommendation` 返回 409（40911）——不要重试再推荐，也不要试图 DELETE（同样 40911）。`PUT`/`DELETE /api/issues/{iid}/recommendation/defer` 是延后/恢复，仅 JWT（API Key 40301）。彻底移除延后项只能由 JWT 用户做。
- `POST /api/projects/{id}/logs` 只接受 API Key。JWT 调用返回 403（40303）。

### checklist 是整组替换

`PUT /api/issues/{iid}/checklist` 提交的 `items` 就是最终清单。没带上的旧条目会被删掉。`items=[]` 会清空清单。

### `q` 是内存全量过滤

`GET /api/projects/{id}/issues` 的 `q` 不是索引查询。它扫全量，匹配标题、正文、作者 login、`INT-xxx` / `GH-xxx`。纯数字还会匹配 `sequence_number` 或 GitHub number。

### 详情自带 collab

`GET /api/issues/{iid}` 的 data 里有 `collab`（`consensus` 和 `summary`）。列表项没有。要看协作区，读详情即可，不必再打一次 `GET /collab`。

### 批量改工作流

`PUT /api/issues/internal-meta` 的请求体是 `{items:[{issue_id, workflow_status}]}`，最多 200 条。某一条失败不会让整批变成 4xx，HTTP 仍是 200，看 `failures[]`。

### collab 先读后写

`PUT` consensus 或 summary 之前先 GET。用户已经清空的块，不要写回去。`DELETE` 幂等，目标本来就没有也返回 200。

追问还没结束时不要写共识。决策问完、用户确认可以开工之后，再 PUT 已经拍板的内容。用户说别问了直接做，共识留空，不要编「按 Issue 原文实施」。未经许可不要关单，不要提交或推送代码。

完成总结写给普通人看，两段就够：之前怎样，现在怎样。不列文件，不贴 commit SHA，不写测试清单。改动还没落到能感知的结果上，就先别写「现在」。

### 请求编码

请求体写成 UTF-8 JSON 文件，用脚本的 `body.json` 传入。不要在 shell 里拼接中文。

### Breaking Changes

- API Key **不可**通过 `PUT /api/issues/:issue_id` 修改 `state` / `state_reason`（open/close）；Agent 应改用 `PUT /api/issues/:issue_id/internal-meta` 的 `workflow_status` 标记完成，open/close 由 JWT 用户在 Web 端操作。
- API Key **不可**发表评论（`POST /api/issues/:issue_id/comments`）。
- API Key **不可** `PUT`/`DELETE /api/issues/:issue_id/ship-hook`。`ship_hook` 仅出现在 Issue GET/列表中供只读。发货后关单/留言由 JWT 用户在 Web 配置，发货成功后由服务器执行。
- 协作区旧端点 `POST/PUT/DELETE /collab/notes`、`/collab/questions` 已移除；`suggestions`、`plan`、`review` 与 `commit_ids` 一并废弃。当前 `GET /collab` 只返回 `consensus` 与 `summary`。

# Fast Ship 项目 ↔ T3 项目绑定（INT-89）

Fast Ship 项目与 T3 项目之间是**显式绑定**关系：用户（或经 Agent 辅助）把某个
Fast Ship 项目绑到某个 T3 环境中的某个 T3 项目上，「启动线程」弹框据此决定在
哪里开线程。不做隐式推断（比对路径名自动配对被否决——歧义交给用户确认）。
项目发现与绑定校验走 HTTP `GET /api/projects`（INT-116 起，零 shell 订阅）。

## 数据模型

表 `project_t3_bindings`，GORM `AutoMigrate` 注册（`server/cmd/server/main.go`），无外置迁移工具。模型在 `server/internal/model/project_t3_binding.go`。

| 字段 | 说明 |
| --- | --- |
| `project_id` | 主键之一；FK → `projects`，`OnDelete: CASCADE` |
| `environment_id` | 主键之一；FK → `t3_environments`，`OnDelete: CASCADE` |
| `t3_project_id` | 该环境中的 T3 项目 ID |
| `t3_project_title` / `workspace_root` | 绑定时快照 |

主键 `(project_id, environment_id)`：每个环境至多绑一个 T3 项目；同一个 Fast
Ship 项目可跨多个环境各绑一个 T3 项目（例如公司机与家用机各绑同名仓库）。

**快照的意义**：`t3_project_title` 与 `workspace_root` 在绑定成功那一刻落库。
环境不可达时列表仍能展示绑定是什么；快照与最新发现不一致（T3 项目被删/改名/
挪路径）即「失效绑定」，前端标红提示重新绑定。

## 端点（JWT + API Key 均可）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/environments/t3-projects` | 只读发现：逐个环境拉 T3 项目列表，返回 `{environment_id, name, status, access, projects:[{id,title,workspace_root}]}` |
| GET | `/api/projects/:id/t3-bindings` | 列出绑定，附环境名与状态 |
| PUT | `/api/projects/:id/t3-bindings/:environmentId` | body `{"t3_project_id":"..."}`；实时校验通过后写入快照 |
| DELETE | `/api/projects/:id/t3-bindings/:environmentId` | 解绑 |

要点：

- **发现与校验走 HTTP `GET /api/projects`（INT-116）**：项目表与 shell 订阅、与
  归档面完全无关——零 shell 调用，归档故障不影响项目发现与绑定校验。端点要求
  `orchestration:read`；401/403 分类为凭证失效（50202「请重新配对」），404 是
  端点缺失/版本不兼容、按上游错误处理（绝不映射成 40418）。HTTP 返回的就是一份
  完整项目表（软删项已在上游下线），没有「活跃/归档合并去重」，也没有
  「归档不可信不下结论」的灰区：项目不在表里即 40401。
- **发现接口有界并行**：多环境并发拉取，各自超时；单环境失败只标在该环境的
  返回上，不影响其他环境的结果——与线程列表的容错口径一致。探测
  （`GET /api/auth/session`）保留，单独回答可达性与凭证有效性。
- **PUT 不保存未校验的绑定**：服务端实时到目标环境确认 T3 项目存在后才写。
  T3 项目不存在返回 40401；环境不可达（网络/凭证失效）返回 50202。
- **API Key 只开放这四个接口**：不含凭证、不含线程内容。线程读写、新建线程、
  附件、配对仍仅 JWT（40301）。这组只读发现面是 API Key 辅助配置的前提。

## Agent 辅助配置流程

写进了 `skills/fast-ship/SKILL.md` 与 `skills/fast-ship/references/api.md`：

1. `GET /api/environments/t3-projects` 列出各环境的 T3 项目；
2. 用自身仓库路径比对各 `workspace_root`，找到自己所在的项目；
3. `PUT /api/projects/:id/t3-bindings/:environmentId` 完成绑定。

Agent 拿不到线程内容，也读不到环境凭证，只能完成「发现 → 比对 → 绑定」这一条路。

## 前端

项目设置新增「T3 绑定」一节：每个环境一行，下拉从发现结果中选 T3 项目，可解绑；
失效绑定标红，提示重新绑定。「启动线程」弹框（见 INT-89）读取绑定列表决定
环境候选与默认值。

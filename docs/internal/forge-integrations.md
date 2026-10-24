# 代码仓集成：Forgejo（INT-166，P0 部署级竖切）

Fast Ship 的第二种代码仓集成：`forge_integrations` 表存 Forgejo/Gitea 实例的
连接配置（机器人账号 + 仓库范围 PAT），管理员在「设置 → 部署」下 CRUD 并做
连接检测。现有 GitHub App 不进这张表——它作为内置 `github-default` 集成**只读
映射**到部署级 App 配置（`deployment_settings` / config.yaml 链路，见
`integrations.md`）。

本期覆盖部署级配置面与项目侧的服务端接线：仓库 URL 解析（`parseRepositoryURL`
承认非 GitHub 主机）、项目 ↔ 集成绑定写入、审查链接按集成构造（见下两节）。
web 设置页、webhook 出站、issue 轮询与 Forgejo 仓库的审查执行面是后续任务；
`pkg/forges` 的 client 已按可复用设计。

## 表结构

`model.ForgeIntegration`（`forge_integrations`，启动 AutoMigrate 建）：

| 列 | 用途 |
| --- | --- |
| `id` | UUID 主键；内置集成固定用 `github-default`（不在表内） |
| `kind` | `forgejo`（本期唯一落库值）；`github` 只出现在内置投影 |
| `name` | 显示名 |
| `base_url` | Forgejo 实例基址，保存时已归一（去尾部 `/`） |
| `api_base_url` | API 基址；省略时按 `{base_url}/api/v1` 推导后**展开落库**，读取侧不再推导（列可空，历史 NULL 与空串同义） |
| `token_encrypted` / `webhook_secret_encrypted` | 各自独立 AES-256-GCM 密文（密钥 `encryption.key`） |
| `ca_cert_pem` | 自定义 CA（可空）；追加系统根证书池，不是替换 |
| `allow_private_network` | 私网放行开关（默认 false），只作用于本集成的 client |

`projects` 加了两个可空引用列 `code_integration_id` / `public_integration_id`：
NULL/空串 = 内置 github-default（现有 GitHub App 链路，存量零迁移）。不建数据
库外键，引用一致性由应用层保证（删除前 `ReferencingProjects` 反查，与
user_github_tokens 同一模式）。

## 项目 ↔ 集成绑定

项目写侧按仓库 URL 的主机派生绑定（`service/project.go` 的
`parseRepositoryURL` + `parseRepoBinding`）：

- **`parseRepositoryURL` 返回 host**：`owner/repo` → `github.com`；
  `github.com/o/r`（任意 scheme、大小写不敏感）→ `github.com`；`host/o/r`、
  `https://host[:port]/o/r` → 小写主机，非 github 主机恒带端口（无显式端口
  按 scheme 补齐：http→80、其余→443，因此 http 集成只有显式 `http://`
  的仓库 URL 才匹配得上）。无 scheme 时首段含 `.` 或 `:` 视为 host，否则
  视为 owner。非 github.com 主机必须匹配某条已配置 forgejo 集成（双方都
  归一成「小写 host:端口」后比较），匹配不到 → 40001「主机未匹配任何已
  配置集成」，不再像旧实现那样把 host 静默解析成 owner。`.git` 后缀与尾
  斜杠的修剪顺序与旧实现
  一致（「`.git` + 尾斜杠」组合剥不到 `.git`，repo 名保留后缀——历史行为照旧）。
- **`repository_url`（公开仓）只允许 GitHub**（host=github.com 或纯
  owner/repo）→ `public_integration_id` 恒为空列。命中 forgejo 集成 → 40001
  且消息点明「公开仓库仅支持 GitHub」（issue 镜像 / release 本期仍是 GitHub
  only）；未知主机同上 40001。
- **`code_repository_url`**：github.com/纯 owner/repo → `code_integration_id`
  空；命中 forgejo → 存该集成 id。Create 与 Update 同口径；Update 三态（key
  缺省保留、显式空把 owner/repo 与集成 id 一起清、非空替换并按新 host 重新
  派生）。
- **归一只对 GitHub 代码仓生效**：`normalizeCodeRepo` 仅在
  `code_integration_id` 为空时把「与公开仓 owner/repo 大小写不敏感相同」的
  code 字段清空——forgejo 代码仓与 github 公开仓同名不是同一仓库，归一会丢
  集成绑定。

## 审查链接按集成构造

PR 链接与 blob 行锚链接统一走 `service/forge_links.go` 的 `forgeLinks`
（kind + base_url → URL 模板），替代此前四处硬编码的 github.com 拼接：

| 集成 | PR | 文件行锚 |
| --- | --- | --- |
| github（内置，base=https://github.com） | `{base}/{o}/{r}/pull/{n}` | `{base}/{o}/{r}/blob/{sha}/{path}#L{n}` |
| forgejo | `{base}/{o}/{r}/pulls/{n}` | `{base}/{o}/{r}/src/commit/{sha}/{path}#L{n}` |

forgejo 模板的依据（Forgejo 是 Gitea 的社区分叉，web 路由一致，核实自 Gitea
`routers/web/web.go` 与 `templates/repo/view_file.tmpl`）：PR 详情路由是
`/{username}/{reponame}/{type:pulls}/{index}`；文件视图是
`/{username}/{reponame}/src/commit/{sha}/{path}`（`/blob/*` 只是被重定向到
`/src/commit/*` 的兼容形态，不稳定）；行锚与 GitHub 同为 `#L{n}`
（`view_file.tmpl`: `id="L{{$line}}"`）。文件路径仍按 `/` 分段 PathEscape，
repo/sha 原样拼接。

review 侧只有 `project_id` / `repo_full_name` 快照，解析链路是
`forgeLinksForProject` / `forgeLinksByProjects`（`ReviewService` 直接查库）：
`project.code_integration_id` 空 = github-default；**绑定的集成行已不存在
（删除集成前应用层拦引用，此处只防并发窗口）或查询失败 → 链接不可解析，
`PullURL`/`BlobLineURL` 返回空串**——审查链接断掉不让读路径/同步失败，但
私有 owner/repo 也不能被拼到 github.com 上发出去。github 兜底只保留给空
集成引用（与零值口径）；`pr_url` 为空串时前端不渲染链接。

## 基址形状与同主机唯一

- **base_url 只收站点根**：路径必须为空或 `/`，否则 40001——审查链接与
  `code_repo_url` 都按 `{base_url}/{owner}/{repo}` 拼接，带路径的基址会拼出
  错误 URL（自托管子路径部署请配反代，或让 API 前缀走 `api_base_url`）。
  **`api_base_url` 不受限**，可以带路径（`/api/v1` 是惯例）。
- **不收 userinfo**：`https://user:pass@host` 直接 40001——凭证随 base_url
  明文落库、回显并被拼进审查链接。保存返回的 base_url 是按 URL 重编码的归一
  串（scheme 小写、去尾斜杠），不是原始输入。
- **同归一主机唯一**：新建与改基址（`forgeBaseHostKey`：小写 host + scheme
  缺省端口，https→443 / http→80）命中其他 forgejo 行即 40001 并点名占用
  集成名。http 与 https 因缺省端口不同算两个主机，与项目侧仓库 URL 的匹配
  口径一致。查重在 service 层先做，数据库不设唯一约束（归一键在 SQL 里算
  不出来）；并发创建两条同主机的窗口接受——后果是多一条集成，不是数据损坏。

## 删除的原子性与残余窗口

`ForgeIntegrationRepository.DeleteIfUnreferenced` 把引用判定写进同一条
`DELETE … WHERE NOT EXISTS (SELECT 1 FROM projects WHERE …)`：删除提交点原子，
「先查后删」之间项目并发绑定成功不会留下悬空引用。service.Delete 拿 0 行受
影响时再查行存在性区分 40435（不存在）与 40935（被引用，消息列项目名）。

接受的残余窗口在 service 层之上：项目侧 `parseRepoBinding` 先按
`forgeIntegrationByHost` 列集成再绑定，删除可以发生在「已匹配、未落库」之间，
后果是项目绑到一个不存在的集成 id（链接构造降级 null，见下节），不是数据
损坏。要彻底关掉得把项目写入与集成行放进同一事务的外键方案，本期不做。

## 项目响应的 code_repo_url

`GET /api/projects*` 的 `Project.code_repo_url`（可空）：无代码仓（owner/repo
缺一）为 null；`code_integration_id` 空按内置 github 拼
`https://github.com/{owner}/{repo}`；绑定 forgejo 集成拼
`{base_url}/{owner}/{repo}`；**绑定的集成行查不到（并发删除窗口）为
null**——绝不回落 github.com，私有 owner/repo 不能被发到 github.com。列表
路径一次批量取回涉及的集成行（`forge_integrations` ListByIDs），没有 N+1。

## 密钥存储

与部署级 GitHub App 同一模式（`integration.go`）：明文只在 service 层内存里
短暂存在，`crypto.Encrypt` 后落库；读接口只回 `token_set` /
`webhook_secret_set` / `has_ca_cert` 标记；解密失败明确 50000，不回落任何明文
来源。token 与 webhook_secret 分两列独立加密——换 PAT 不触碰验签密钥的密文。

写接口三态：

- `token` / `webhook_secret`：缺省、null、空串都 = 保留旧值（空串不清空；
  这两个密钥没有「显式清空」的 UI 语义，删集成即可整体作废）。
- `ca_cert_pem`：缺省 = 保留，空串 = 清空，其余替换并校验 PEM 可解析。
- `api_base_url`：缺省/null = 保留，空串 = 重置为 `{base_url}/api/v1` 推导。

## SSRF 规则（`pkg/forges`）

Forgejo 基址是管理员可填的任意 URL，必须把「内网探测」挡在 client 层：

- **协议**：默认仅 https；http 只在 `allow_private_network=true` 时对该集成可用。
- **保存时 DNS 校验**：基址解析后逐 IP 过拒绝段，任一命中即 40001；解析失败
  同样拒绝（保存时连不上的基址不进库）。
- **拨号时 IP 校验**：`net.Dialer.Control` 在 DNS 解析后、建连前判定实际目标
  IP——重定向后的再连接、DNS rebinding 后的第二次解析都拦得住。拒绝段：回环、
  RFC1918、ULA fc00::/7、链路本地 fe80::/10 与 169.254.0.0/16（含云元数据
  169.254.169.254）、多播/未指定、CGNAT、0.0.0.0/8、192.0.0.0/24、240.0.0.0/4、
  已废弃 fec0::/10，以及内嵌 IPv4 的 IPv6（mapped/6to4/Teredo/NAT64 解出载荷
  递归判定）。
- **私网放行的粒度是主机名，且永不关掉拨号校验**：`allow_private_network`
  =true 时只有本集成 base_url / api_base_url 的 host（`Config.AllowedHosts`）
  的拨号按**私网口径**判定，名单外主机照常走公网口径——开关放行的是「这个
  Forgejo 在内网」，不是「随便连内网」。两种口径下**每次拨号都过
  `net.Dialer.Control`**（DNS 解析后、建连前）：私网口径只放宽 IP 段表，回环、
  链路本地（含元数据 169.254.169.254）、多播、未指定与保留段照拒，保存后 DNS
  改指、rebinding 后的第二次解析照样拦。私网口径放行的段：RFC1918、ULA
  fc00::/7、CGNAT 100.64.0.0/10 与公网。
- **重定向规则**：目标 host:port 必须与首个请求一致（跨主机拒绝）；未开私网
  放行时每一跳都必须 https；**任何配置下 https→http 的降级跳转都拒绝**
  （`ErrSchemeDowngrade`——明文 http 只该来自管理员配置的基址，不该被响应
  悄悄带出来）。
- **自定义 CA 追加系统池**：公网签发的证书照常验证；**不使用
  InsecureSkipVerify**（测试里机器断言了这一点）。
- 统一 `Proxy = nil`：走环境代理时校验到的会是代理地址，防线失效（与
  `githubmedia` 的 guard 同一取舍）。
- 每集成独立 `*http.Client`（超时 10s、CA、拨号校验都按行构建），P1 的
  webhook/轮询直接复用 `forges.NewClient`。

## 内置 github-default 语义

列表首条恒为它：`kind=github`、`base_url=https://github.com`、`api_base_url`
取部署 App 的 API base（config 缺省 https://api.github.com）、`builtin=true`。
`token_set` 映射「App 身份当前可用」（`IntegrationService.Current` 的 ok），
`webhook_secret_set` 映射当前生效验签密钥非空。它不可修改、不可删除、不可走
forge test 端点（40001，test 引导去 `POST /api/deployment/github-app/test`）；
写侧修改永远走既有 deployment/github-app 端点，**不搬数据、不建影子行**。
App 密文解密失败时列表整体 50000——与 integrations.md「不回落 config」同一规则。

## 路由与错误码

`/api/deployment/integrations`（GET 列表 / POST 创建）、`/:id`（GET / PUT /
DELETE）、`/:id/test`（POST 检测），全部挂管理员组（JWT + RequireAdmin；API
Key 在 RequireJWT 已被 40301 拒）。

| 码 | 语义 |
| --- | --- |
| 40435 | 集成不存在 |
| 40935 | 仍被项目引用，消息列出引用项目名 |
| 40001 | 参数/校验失败（名称、基址 SSRF、CA PEM、内置集成写保护等，消息带具体原因） |

连接检测：解密 token 后调 `GET {api_base_url}/version` 与 `GET {api_base_url}/user`，
返回 `{status, version, username, detail}`。status 沿用 `unchecked / ok /
invalid / failed` 口径（unchecked 不会由该端点产生）：ok=两个请求都成功；
invalid=401/403（PAT 被拒）；failed=网络错误或其他状态码。**结论只随响应返回、
不落库**（表里没有检测三列——没有后台轮询去消费它，落库只会制造过期状态）。
检测走的是同一个 forges client，私网/自签 CA 的实例因此天然可测。

## 本期不做（后续任务）

- 设置页卡片（web）。
- Forgejo webhook 出站、issue 轮询、Forgejo 仓库的 PR 审查执行面（派发、
  diff 读取、行内 review 等 GitHub API 依赖仍是 GitHub only；P1 复用
  `pkg/forges`）。
- 检测结论落库与定时巡检。

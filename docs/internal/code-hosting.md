# 代码托管运维

代码源、镜像与 PR 的日常操作约定见根目录 `AGENTS.md` 的「代码托管与 PR」。本文记录服务接线、迁移记录与恢复入口。

## GitHub 推送镜像

Forgejo 收到提交后经 **SSH 部署密钥**推送到 GitHub 公开仓库 `gitbobobo/fast_ship`（sync on commit，间隔兜底 8h）。部署密钥标题「Forgejo NAS push mirror」，登记在该 GitHub 仓库的 Deploy keys（可写）；私钥只存 Forgejo 实例。镜像停同步先看 Forgejo 仓库设置里的推送镜像 `last_error`；分支删除不自动触发同步，需手动调 `POST /api/v1/repos/fjbobo/fast_ship/push_mirrors-sync`。GitHub 上的 CI（`.github/workflows/ci.yml`）只对镜像同步来的 main 做事后复核，不承担 PR 检查——PR 检查由本地代替，见 AGENTS.md。

GitHub 侧继续承担两类写服务，不受「代码只读」约束：公开 Issue 服务（Fast Ship 的 issue 同步经 GitHub App 持续写入）与 Docker Publish（`v*` tag 经镜像同步到 GitHub 后触发 Actions 推 `ghcr.io/gitbobobo/fast_ship`）。除这两类与镜像自动同步外，不向该仓库写入任何东西；推镜像没有分支过滤，其它来源的改动会被下次同步覆盖。

## Forgejo 备份

Forgejo 是唯一代码源，PR/issue/review 历史只在实例上。备份对象：NAS 的 `/share/Container/forgejo/data` 整目录（含 `gitea/gitea.db`、全部仓库与 LFS，以及 `gitea/conf/app.ini` 内的 `SECRET_KEY` 等密钥——密钥丢失会导致 2FA 与加密数据不可解，务必与数据同处保管）。目前没有自动备份（待办）。热拷贝会得到撕裂的 SQLite，在线备份用 `forgejo dump`（或对 `gitea.db` 做 `VACUUM INTO`）；整目录拷贝必须先停容器。恢复后校验 `PRAGMA integrity_check`、镜像 last_error 与仓库数量。

## Fast Ship（自托管即自用）

- 部署级集成 `NAS Forgejo`（id `321b5b36-01d6-4260-832d-c2596396fbfc`）及机器人 `fastship-bot`（协作者 write）已配置。本项目代码仓已改为 `https://fj.frp.musiver.cn/fjbobo/fast_ship`，原审查组合、环境与触发器保持不变，审查已重新开启并读回确认。首个 Forgejo PR #157 的审查重跑已受理，返回的 head SHA 与 Forgejo 实时提交一致。issue 管理与同步保留 GitHub 公开仓：`repository_url` 不变；公开仓只允许 GitHub 绑定，代码仓绑定按主机派生（见 `forge-integrations.md`）。
- 仓库 webhook 指向 `https://ship.frp.musiver.cn/api/forge/<集成id>/webhooks`，订阅 **Pull Request、Pull Request Sync、Pull Request Comment**（`issue_comment`，评论唤醒）三项；服务端先验签（HMAC-SHA256，hex 无前缀）再入队。secret 与集成一致，轮换时集成页改完同步改各仓库 webhook。
- GitHub 仓库没有单独配置仓库 webhook（迁移前就没有）；GitHub App 的 webhook 与后台轮询是独立链路，不能据此断言 GitHub 审查已停。本次已通过项目代码集成及审查设置读回确认后续审查走 Forgejo。不要卸载公开 issue 同步依赖的 GitHub App。issue↔PR 关联的状态刷新（`pull-requests/sync`）只认 GitHub PR，对 Forgejo PR 不可用（表上 `provider` 列已留位）。
- 入站失败先读 Forgejo webhook 的投递记录（实例 `gitea.db` 的 `hook_task` 表），再查 Fast Ship 审查记录；5 分钟轮询补漏，不能把轮询成功当成 webhook 已正常投递。
- NAS 将服务域名解析到内网地址；Fast Ship 集成开启该实例的内网访问，Forgejo `app.ini` 的 `[webhook] ALLOWED_HOST_LIST` 放行 `ship.frp.musiver.cn`。

## 发版链路（Forgejo 后）

`make release` 原子推送 Forgejo（main + tag）；GitHub 构建 tag 到达镜像后才触发（sync on commit 异步）。release.sh 推送成功即退出回滚区间，随后不访问网络，直接输出构建页面和本次提交/tag 的查询命令；镜像仓库从 `github` 远程的 fetch URL 解析（push 已置 `disabled://`）。详见 [发版](release.md)。

## 审查跟进

认证用本机 git 凭据里的 Forgejo 令牌，且只在管道与权限 600 的临时文件里流转：`git credential fill`（stdin 给 `protocol=https` 与 `host=fj.frp.musiver.cn`）的输出转成 curl `-K` 配置文件或 `-H @头文件`（用完即删），不要单独执行它（会把令牌打进 stdout）、不要把令牌写进命令行参数。以下均为 `https://fj.frp.musiver.cn/api/v1` 下的调用：

- 审查状态：`GET /repos/fjbobo/fast_ship/commits/<sha>/statuses`，context 为「代码审查」/「视觉审查」，核对最新 HEAD
- 轮次汇总：`GET /repos/fjbobo/fast_ship/issues/<PR号>/comments`（PR 评论走 issue 接口），Fast Ship 每轮发一条汇总评论；对意见的回应也用这个接口 `POST` 顶层评论并引用原意见
- 行内意见：`GET /repos/fjbobo/fast_ship/pulls/<PR号>/reviews` 拿 review 列表，逐条 `GET /repos/fjbobo/fast_ship/pulls/<PR号>/reviews/<id>/comments` 取文件与行号；行内评论没有回复线程，不要对它 `POST`
- 项目改绑且审查开启后，推送新提交经 webhook（`pull_request_sync`）自动重审；顶层评论 @fast-ship-bot 唤醒新一轮。跟进时核对最新 HEAD 的实际产出，不能只凭 webhook 受理就认定审查已完成

`$pr-review-loop` 的通用流程（轮次判定、意见收敛）仍适用，但其 GitHub 命令在 Forgejo PR 上不可用，一律换成本节接口。

## 迁移记录（2026-10-31，gitbobobo/fast_ship → fjbobo/fast_ship）

- 迁移脚本 `~/work/scripts/forgejo-migrate.py`（未入库，webhook 事件已按本项目文档补第三项 `issue_comment`）。`POST /repos/migrate` 经 frp 代理 504 属预期：迁移在服务端继续，等 issue/PR 数对上 GitHub 基线（28 issue + 128 PR）后重跑脚本（幂等）即完成 refs 核对、镜像、机器人、webhook 与本地切换。
- 迁移结果：双端 `git ls-remote --heads --tags` 一致（9 分支 + 67 tag）；历史数据 156 条 issue（含 128 PR）、1867 条评论、9 个标签全部迁入；67 个轻量 tag 被 Forgejo 转成 release 条目展示（GitHub 侧仍无 Release，属迁移产物的正常形态）。GitHub 仓库 description 已改为「Read-only code mirror …」。
- **`issue_index` 缺行（本仓库实踩，payment 是错值、本仓库是整行缺失）**：迁移完成后 `issue_index` 里没有 `group_id=<repo_id>` 的行，下一次建 issue/PR 的 UPSERT 从 1 起步会撞已迁移索引。修复在 NAS 容器内执行：`docker exec -i forgejo sqlite3 /data/gitea/gitea.db "INSERT INTO issue_index (group_id, max_index) VALUES (<repo_id>, (SELECT MAX(\"index\") FROM issue WHERE repo_id=<repo_id>)) ON CONFLICT(group_id) DO UPDATE SET max_index=(SELECT MAX(\"index\") FROM issue WHERE repo_id=<repo_id>);"`，修完核对 `max_index = MAX(issue.index)`。
- **迁移期 PR 评论的行内定位可能降级**：服务端日志出现成片 `GetRepoRawDiffForFile … io: read/write on closed pipe` 警告（[W] 级，单条评论非致命），历史行内评论的行号锚点可能缺失；历史归档可接受，新审查不受影响。
- 迁移前 GitHub 仓库无单独的仓库 webhook、无开放 PR、无 Release、无 LFS；公开 issue 同步所依赖的 GitHub App 保留。审查切换仍以项目改绑、恢复审查及 Forgejo PR 实际产出为准。

## 重复迁移

迁移脚本存放在执行过迁移的那台机器；其他机器直接按上两节的接口手工执行：迁移仓库、核对引用（`git ls-remote` 源目标一致才继续）、配置镜像、加机器人协作者、配 webhook、切换本地远程。脚本可按参数自动完成以上全部，运行前阅读 `--help`；部署级集成由管理员单独配置。本仓库走 SSH 部署密钥镜像（`gitbobobo` 名下仓库部署密钥可用，与 `liuyincs` 组织仓库的 HTTPS 令牌镜像方案不同）。工作区审查机 ben 的克隆已切 Forgejo origin；新克隆没有 `github` 远程属正常。

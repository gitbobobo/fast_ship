# Fast Ship

Fast Ship 是一个项目管理工具，用于记录、同步多个项目的需求及其状态。

## Bobo 的留言

我喜欢有远见的想法、简单的系统，以及那些使用起来直观易懂的软件。不要仅仅因为某种复杂性已经存在，就继续保留它。也不要仅仅因为某种架构看起来很复杂而将其纳入设计中。要了解真正的限制条件，然后努力找到最简单的解决方案，让系统的行为符合预期。

本文档的其余部分旨在帮助你更好地理解代码结构，并能更有效地进行修改。请把这些说明视为“合理的默认设置”，而非“必须遵守的硬性规则”。

## 代码托管与 PR

- 唯一代码源是 `https://fj.frp.musiver.cn/fjbobo/fast_ship`，提交推送与 PR 均在 Forgejo。自检：`git remote get-url origin` 与 `git remote get-url --push --all origin`（全部推送地址）去掉尾部 `.git` 后均应为该地址。迁移旧克隆时先 `git remote set-url origin https://fj.frp.musiver.cn/fjbobo/fast_ship.git`，再清除全部独立推送地址：`git config --unset-all remote.origin.pushurl`（退出码 5 表示原本没有该项），复查实际推送地址，避免 fetch 已改而 push 仍指 GitHub。已有 `github` 远程时，其全部 push URL 应仅有 `disabled://github-read-only`；新克隆没有 `github` 远程属正常，无需补建。
- `https://github.com/gitbobobo/fast_ship` 是 Forgejo 的推送镜像（SSH 部署密钥，sync on commit），禁止直接向它推送；公开 Issue 服务与 GitHub Actions 的 Docker Publish（`ghcr.io/gitbobobo/fast_ship`）继续走 GitHub。推送镜像没有分支过滤，GitHub 侧任何非同步来源的改动都会被下次同步覆盖。
- PR 在 Forgejo 创建，云端审查由 Fast Ship 自己的 `NAS Forgejo` 集成触发。项目代码仓已改绑并读回确认审查开启，公开 Issue 仓仍保留 GitHub。Forgejo 不接远端 CI，**PR 检查由本地代替**：合并前在 `web/` 跑 `pnpm api-types --check` 与 `pnpm check`（lint + typecheck）；后端未改动时无需全仓 Go 测试，有改动时另跑对应包的 `go test`。
- 发版仍用 `make release`：原子推送 Forgejo 后，脚本从 `github` 远程的 fetch URL 解析镜像仓库，立即输出构建页面和限定本次提交 SHA、tag 的查询命令，不等待镜像或查询 GitHub。推送成功不代表镜像同步或构建成功，详见[发版](docs/internal/release.md)与[代码托管运维](docs/internal/code-hosting.md)。
- 修改发版脚本或其测试时，合并前另跑 `make test-release`；它只使用临时裸仓库，不发布真实版本。

## 开发与验证

在我没有明确说明时，禁止提交代码或创建 PR。

## 端到端验证

这台电脑是我正在使用的工作电脑，不是 CI 机，同一台机器上还可能有别的代理在并行工作。端到端验证和截图不能抢占前台、不能弹出系统权限框、不能影响其他任务。这一节是硬性规则，派给子代理时要原样转述。

- 用 `make dev` 启动后，只用无头 Playwright 或 Chromium 截图；不用 `open` 打开网址，不启动有界面的浏览器。
- 不用 `osascript`、System Events、`cliclick`、合成键盘鼠标事件、`screencapture`、读取窗口列表。
- 不用 `sudo`，不 `killall` 或 `kill -9` 系统服务；只操作本任务启动的进程和容器。
- 「改动前」截图：用 `git worktree add` 在基线提交上建临时工作树，完整构建后截图，用完删除。不要在功能分支里切回基线提交。
- 放弃规则：一个场景 15 分钟内无法用上面的方式截到，或同一个崩溃出现两次，就停止尝试，在 PR 中写明无法截图的原因和替代证据（如测试、日志、接口响应）。

## 习惯

- 不要将实施计划、研究笔记或临时文件保存在工作树中。请把所有临时性工作资料保存在工作树之外。
- 将可靠的架构、各种限制条件以及相关决策都记录在 docs/internal/ 中。当产品发生变更时，要及时更新这些文档，这样，各个代理就能获取到最新的信息，而不会依赖过时的数据或计划。
- 在开发途中发现的残留项，我更倾向于直接清理，而不是留到后面累积成山。

## 品味

- 复杂性应该被控制在适配器的层面。协调机制应保持简单纯粹，用户界面则应保持直观易用。
- 注释用于描述某个功能的用法，以及代码执行时的各种操作。注释主要用于描述函数的用途，而非对每一行代码都进行详细说明。

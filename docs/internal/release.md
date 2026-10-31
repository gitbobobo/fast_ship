# 发版（Release）

发版唯一入口是 `make release`（Makefile 委托 `scripts/release.sh`）。一次发版产出一个只改 VERSION 的 `chore: bump version to X` 提交和一个轻量 tag `vX`，推送到 Forgejo（唯一代码源）后由推送镜像同步到 GitHub，触发 GitHub Actions 构建镜像推 GHCR。不做 changelog，不建 Release。

## 用法

- `make release`：VERSION 补丁号 +1（如 0.1.42 → 0.1.43）。
- `make release VERSION=0.2.0`：指定版本，误写的前导 `v` 会被去掉。`VERSION=0.2.0 make release` 等效。

VERSION 文件当前内容与目标版本都必须是 `X.Y.Z` 形式，否则报错退出。显式指定的版本必须高于当前 VERSION。

`VERSION` 也可经环境变量传入（`VERSION=x.y.z make release` 即是此机制）；若 shell 已导出同名变量会当作显式版本，执行前确认脚本打印的目标版本号。

## 前置检查

四条全过才动手，任一不满足即报错退出，不产生提交或 tag：

1. 当前分支是 `main`（detached HEAD 报错）。
2. `git status --porcelain` 为空，含 untracked 文件。
3. 查询远程 main（origin 配了 pushurl 时以 pushurl 为准，即实际推送目标）与本地 HEAD 相同，走 ls-remote，不改本地引用。
4. 目标 tag 本地与远程都不存在，且目标版本高于远程最高 `v*` tag。

前两条是本地检查，后两条需要网络。远程检查与 tag 高度基线都只看 Forgejo（origin）——镜像可能滞后，不作基准。

## 产物与推送

脚本依次执行：写 VERSION → `git commit`（信息 `chore: bump version to X`，只含 VERSION）→ `git tag vX`（轻量 tag，与现有 tag 一致）→ 一条 `git push --atomic` 同推 main 与 tag。写 VERSION 到推送之间任一步失败会自动回滚本地提交与 tag（远端由 --atomic 保证无半成品），修复原因后可直接重跑。

`--atomic` 只覆盖 Forgejo 端。GitHub 镜像按 sync on commit 异步同步，不在原子区间内：推送成功即退出回滚区间，此后镜像同步或构建查询的任何失败都只报告，不回滚、不删 tag、不重推。

## 构建查询（推送后）

推送成功后脚本立即输出 Docker Publish 页面和查询命令，不再进行网络查询或等待：

- origin 是 GitHub：按 origin 解析构建页面（迁移前的旧仓库形态仍可发版）。
- origin 是 Forgejo：改从 `github` 远程的 **fetch URL** 解析镜像仓库（`github` 远程的 push 已置 `disabled://`，不能用于解析与查询）；解析不到则跳过查询。
- Forgejo origin 下镜像异步同步：输出页面并不代表 tag 已到 GitHub，也不代表构建已启动。长时间未同步时检查 Forgejo 推送镜像的 `last_error`，必要时手动触发同步；不要为此重新发版。脚本提供的 `gh run list` 命令同时限定本次提交 SHA 与 tag，避免匹配镜像上同名旧 tag 的构建。

脚本不依赖 gh；安装并登录 gh 后可运行输出的查询命令，也可直接打开构建页面。`v*` tag 触发 `.github/workflows/docker-publish.yml`：先跑 `pnpm check`，再构建 linux/amd64 与 linux/arm64 镜像推到 `ghcr.io/gitbobobo/fast_ship`，镜像 tag 为版本号和 `latest`，全程约 10 分钟以上。

`scripts/release-fixture.sh` 是发版脚本的隔离测试：临时裸仓库当远端，覆盖 GitHub origin、Forgejo origin + 禁用 GitHub 推送、新克隆没有镜像配置及推送被拒回滚。测试让推送后的网络调用和 gh 失败，断言脚本完全不调用它们，同时核对真实远端 main/tag 和本地 VERSION/tag；改 release.sh 后跑一遍。

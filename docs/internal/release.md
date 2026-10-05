# 发版（Release）

发版唯一入口是 `make release`（Makefile 委托 `scripts/release.sh`）。一次发版产出一个只改 VERSION 的 `chore: bump version to X` 提交和一个轻量 tag `vX`，推送后由 GitHub Actions 构建镜像推 GHCR。不做 changelog，不建 GitHub Release。

## 用法

- `make release`：VERSION 补丁号 +1（如 0.1.42 → 0.1.43）。
- `make release VERSION=0.2.0`：指定版本，误写的前导 `v` 会被去掉。`VERSION=0.2.0 make release` 等效。

VERSION 文件当前内容与目标版本都必须是 `X.Y.Z` 形式，否则报错退出。显式指定的版本必须高于当前 VERSION。

`VERSION` 也可经环境变量传入（`VERSION=x.y.z make release` 即是此机制）；若 shell 已导出同名变量会当作显式版本，执行前确认脚本打印的目标版本号。

## 前置检查

四条全过才动手，任一不满足即报错退出，不产生提交或 tag：

1. 当前分支是 `main`（detached HEAD 报错）。
2. `git status --porcelain` 为空，含 untracked 文件。
3. 查询远程 main（origin 配了 pushurl 时以 pushurl 为准）与本地 HEAD 相同，走 ls-remote，不改本地引用。
4. 目标 tag 本地与远程都不存在，且目标版本高于远程最高 `v*` tag。

前两条是本地检查，后两条需要网络。

## 产物与构建

脚本依次执行：写 VERSION → `git commit`（信息 `chore: bump version to X`，只含 VERSION）→ `git tag vX`（轻量 tag，与现有 tag 一致）→ 一条 `git push --atomic` 同推 main 与 tag。写 VERSION 到推送之间任一步失败会自动回滚本地提交与 tag（远程由 --atomic 保证无半成品），修复原因后可直接重跑。

`v*` tag 触发 `.github/workflows/docker-publish.yml`：先跑 `pnpm check`，再构建 linux/amd64 与 linux/arm64 镜像推到 `ghcr.io/<owner>/<repo>`，镜像 tag 为版本号和 `latest`，全程约 10 分钟以上。脚本不等构建，推送后尝试用 `gh run list` 轮询约 60 秒找到对应 run 并打印链接；gh 不可用或非 GitHub 远程时降级为打印 workflow 页面链接。想盯进度用 `gh run watch`。

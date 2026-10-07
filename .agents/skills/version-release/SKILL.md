---
name: version-release
description: Fast Ship 发版。唯一入口是 make release（scripts/release.sh），自动完成版本 bump、提交、tag、原子推送并触发 Docker 镜像构建。当用户要求发版、升级版本、打 tag 或发布新版本时使用。
---

# Fast Ship 版本发布

## 何时使用

当用户要求你：
- 升级系统版本号并发布
- 打版本 tag 并构建 Docker 镜像
- 执行完整的版本发布工作流

## 版本号来源

仓库根目录 [VERSION](/VERSION) 是系统版本的唯一来源，格式为 `x.y.z`（不含 `v` 前缀）。

- 前端侧边栏显示为 `v{x.y.z}`
- Git tag 使用 `v` 前缀，例如 `v0.1.33`
- 新目标版本必须高于当前 VERSION，`make release` 会强制校验

## 发布流程

在 main 分支、工作区干净、与远程同步的状态下运行：

```bash
make release                # VERSION 补丁号 +1
make release VERSION=x.y.z  # 指定版本
```

脚本完成前置检查、VERSION 修改、`chore: bump version to x.y.z` 提交、`vx.y.z` tag，并以一条 `git push --atomic` 同推 main 与 tag，最后打印 Docker Publish 的 run 链接。前置检查、产物与构建链路的完整约定见 [docs/internal/release.md](/docs/internal/release.md)。

## 失败处理

- **前置检查报错或发版中途失败**：守卫失败零副作用；写 VERSION 到推送之间失败会自动回滚本地提交与 tag。按报错修复原因后重跑 `make release`。
- **Docker 构建失败**：`gh run view --log-failed` 排查。需要代码修复就合入后用新版本号重跑 `make release`；仅需重试构建时在 Actions 页面 rerun。
- **禁止**使用 `git push --force` 覆盖已发布的 tag，除非用户明确要求。

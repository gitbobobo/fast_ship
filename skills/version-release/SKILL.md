---
name: version-release
description: 升级 Fast Ship 系统版本号并完成发布流程。包括更新 VERSION、提交推送、等待 CI、打 tag 推送、等待 Docker 镜像构建。当用户要求发版、升级版本、打 tag 或发布新版本时使用。
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

发布前确认目标版本号：读取 `VERSION` 与 `git tag -l 'v*' | sort -V | tail -1`，新版本应高于当前最新 tag。

## 发布流程

按顺序执行，任一步失败则修复问题后从失败步骤重试，不要跳步。

```
任务进度：
- [ ] 1. 更新 VERSION
- [ ] 2. 提交并推送到远程
- [ ] 3. 等待 CI 通过
- [ ] 4. 打 tag 并推送到远程
- [ ] 5. 等待 Docker 镜像构建成功
```

### 步骤 1：更新 VERSION

编辑根目录 `VERSION`，写入新版本号（仅 `x.y.z`）。

### 步骤 2：提交并推送

1. 确认工作区仅包含版本相关变更：`git status --porcelain`
2. 提交（Conventional Commits）：

```bash
git add VERSION
git commit -m "$(cat <<'EOF'
chore: bump version to x.y.z

EOF
)"
```

3. 推送到远程：

```bash
git push origin main
```

若推送被拒绝，先按 git-sync 工作流同步远程后再推送。

### 步骤 3：等待 CI 通过

CI 工作流：[.github/workflows/ci.yml](/.github/workflows/ci.yml)，在 `main` 分支 push 时触发。

```bash
# 查看最近一次 CI 运行
gh run list --workflow=ci.yml --limit 1

# 等待完成（将 RUN_ID 替换为实际 ID）
gh run watch RUN_ID
```

CI 包含 Web Check（`pnpm check`）。若失败：
1. 查看日志：`gh run view RUN_ID --log-failed`
2. 修复问题并提交推送
3. 重新等待新的 CI 通过后再进入步骤 4

### 步骤 4：打 tag 并推送

仅在步骤 3 CI 成功后执行：

```bash
git tag vx.y.z
git push origin vx.y.z
```

tag 必须与 `VERSION` 内容一致（加 `v` 前缀）。若 tag 已存在，确认是否误操作；需要重新发布时使用更高的版本号。

### 步骤 5：等待 Docker 镜像构建成功

Docker 工作流：[.github/workflows/docker-publish.yml](/.github/workflows/docker-publish.yml)，在推送 `v*` tag 时触发。

```bash
# 查看 Docker 发布运行
gh run list --workflow=docker-publish.yml --limit 1

# 等待完成
gh run watch RUN_ID
```

构建成功后镜像发布到 `ghcr.io/<owner>/<repo>`，带对应 tag 和 `latest`。

若失败：
1. 查看日志：`gh run view RUN_ID --log-failed`
2. 修复问题（常见：前端检查失败、Dockerfile 构建错误）
3. 若修复需要新提交：回到步骤 1，使用新的补丁版本号重新走完整流程
4. 若仅需重试构建：删除远程 tag 后重新推送，或修复后打新 tag

## 失败重试原则

- **CI 失败**：修复代码 → 提交推送 → 重新等待 CI → 再继续打 tag
- **Docker 构建失败**：先判断是否需要代码修复；需要则 bump 版本重走全流程，不需要则重试 tag 推送
- **禁止**在 CI 未通过时打 tag
- **禁止**使用 `git push --force` 覆盖已发布的 tag，除非用户明确要求

#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

VERSION_FILE="${ROOT_DIR}/VERSION"
SEMVER_RE='^(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})$'

err() {
  printf '错误：%s\n' "$1" >&2
  exit 1
}

# 发版步骤失败时回滚本地改动：删本地 tag、重置到发版前 HEAD（VERSION 一并恢复）。
# 不调 exit，由 set -e 用原命令退出码结束；内部命令一律 || true 防二次触发 ERR。
release_rollback() {
  printf '错误：发版中断，已自动回滚本地改动，修复原因后可重跑\n' >&2
  git tag -d "${TAG_NAME}" >/dev/null 2>&1 || true
  git reset --hard "${PREV_HEAD}" >/dev/null 2>&1 || true
}

# 版本比较：$1 严格高于 $2 时返回 0。调用前两侧均已过 SEMVER_RE（无前导零），(( )) 十进制安全
version_gt() {
  local a1 a2 a3 b1 b2 b3
  IFS='.' read -r a1 a2 a3 <<< "$1"
  IFS='.' read -r b1 b2 b3 <<< "$2"
  (( a1 > b1 )) && return 0
  (( a1 == b1 && a2 > b2 )) && return 0
  (( a1 == b1 && a2 == b2 && a3 > b3 )) && return 0
  return 1
}

[[ $# -le 1 ]] || err "用法：$0 [version]，例如 $0 0.2.0"

# 前置检查：本地检查在前，网络检查在后
BRANCH="$(git branch --show-current)"
[[ "${BRANCH}" == "main" ]] || err "当前分支不是 main（当前：${BRANCH:-detached HEAD}），请切换到 main 再发版"

STATUS="$(git status --porcelain)" || err "无法读取工作区状态"
[[ -z "${STATUS}" ]] || err "工作区有未提交的改动，请先提交或清理"

# 目标版本：参数优先（允许误写前导 v），否则取 VERSION 补丁号 +1
[[ -f "${VERSION_FILE}" ]] || err "VERSION 文件不存在"
CURRENT="$(cat "${VERSION_FILE}")"
[[ "${CURRENT}" =~ ${SEMVER_RE} ]] || err "VERSION 文件内容不是合法版本号：${CURRENT}"

if [[ -n "${1:-}" ]]; then
  VERSION="${1#v}"
  [[ "${VERSION}" =~ ${SEMVER_RE} ]] || err "无效的版本号：$1（应为 X.Y.Z）"
  version_gt "${VERSION}" "${CURRENT}" || err "目标版本 ${VERSION} 不高于当前版本 ${CURRENT}"
else
  IFS='.' read -r V_MAJOR V_MINOR V_PATCH <<< "${CURRENT}"
  VERSION="${V_MAJOR}.${V_MINOR}.$((V_PATCH + 1))"
  [[ "${VERSION}" =~ ${SEMVER_RE} ]] || err "自动递增后的版本号 ${VERSION} 超出位数限制，请显式指定版本"
fi
TAG_NAME="v${VERSION}"

# 远程检查针对实际推送目标（pushurl 优先），用 ls-remote 查询，不改本地引用
PUSH_URL="$(git config --get remote.origin.pushurl || git config --get remote.origin.url || true)"
[[ -n "${PUSH_URL}" ]] || err "未配置 origin 远程"

REMOTE_MAIN="$(git ls-remote "${PUSH_URL}" refs/heads/main | cut -f1)" || err "无法查询远程 main"
[[ -n "${REMOTE_MAIN}" ]] || err "远程不存在 main 分支"
[[ "$(git rev-parse HEAD)" == "${REMOTE_MAIN}" ]] || err "本地 HEAD 与远程 main 不一致，请先同步"

if git rev-parse -q --verify "refs/tags/${TAG_NAME}" >/dev/null; then
  err "tag ${TAG_NAME} 本地已存在"
fi
REMOTE_TAG="$(git ls-remote --tags "${PUSH_URL}" "refs/tags/${TAG_NAME}")" || err "无法查询远程 tag，请检查网络与 origin 配置"
[[ -z "${REMOTE_TAG}" ]] || err "tag ${TAG_NAME} 远程已存在"

# 目标版本必须高于远程现有最高 v* tag，防 latest 镜像回退；非 semver tag 不计入
ALL_TAGS="$(git ls-remote --tags "${PUSH_URL}")" || err "无法查询远程 tag 列表，请检查网络与 origin 配置"
LATEST_TAG=""
while IFS= read -r TAG_VER; do
  if [[ -z "${LATEST_TAG}" ]] || version_gt "${TAG_VER}" "${LATEST_TAG}"; then
    LATEST_TAG="${TAG_VER}"
  fi
done <<< "$(printf '%s\n' "${ALL_TAGS}" | grep -v '\^{}' | cut -f2 | sed -n 's|^refs/tags/v\(.*\)$|\1|p' | grep -E "${SEMVER_RE}" || true)"

if [[ -n "${LATEST_TAG}" ]]; then
  version_gt "${VERSION}" "${LATEST_TAG}" || err "目标版本 ${VERSION} 不高于现有最高 tag v${LATEST_TAG}"
fi

PREV_HEAD="$(git rev-parse HEAD)"
printf '发版：%s -> %s\n' "${CURRENT}" "${VERSION}"

# 写 VERSION 到推送之间任一失败或中断自动回滚本地改动（远程由 --atomic 保证无半成品）
trap release_rollback ERR
trap 'release_rollback; exit 130' INT
trap 'release_rollback; exit 143' TERM
printf '%s\n' "${VERSION}" > "${VERSION_FILE}"
git add VERSION
git commit -m "chore: bump version to ${VERSION}"
git tag "${TAG_NAME}"
git push --atomic origin main "refs/tags/${TAG_NAME}"
# 推送成功即退出回滚区间：此后任何失败只报告，不回滚、不删 tag、不重推
trap - ERR INT TERM

printf '已推送 main 与 tag %s\n' "${TAG_NAME}"

# 推送后打印 Docker Publish 的 run 链接，不等待构建完成。
# origin 是 GitHub 时按推送地址查询；origin 是 Forgejo 等其它主机时，改从
# github 远程的 fetch URL 解析镜像仓库（github 远程的 push 已置 disabled，
# 不能用于解析与查询）。
GH_URL=""
case "${PUSH_URL}" in
  https://github.com/*|http://github.com/*|git@github.com:*|ssh://git@github.com/*)
    GH_URL="${PUSH_URL}" ;;
  *)
    GH_URL="$(git remote get-url github 2>/dev/null || true)" ;;
esac
REPO_PATH=""
case "${GH_URL}" in
  https://github.com/*)   REPO_PATH="${GH_URL#https://github.com/}" ;;
  http://github.com/*)    REPO_PATH="${GH_URL#http://github.com/}" ;;
  git@github.com:*)       REPO_PATH="${GH_URL#git@github.com:}" ;;
  ssh://git@github.com/*) REPO_PATH="${GH_URL#ssh://git@github.com/}" ;;
esac
REPO_PATH="${REPO_PATH%.git}"
REPO_PATH="${REPO_PATH%/}"

if [[ -z "${REPO_PATH}" ]]; then
  printf '未能解析 GitHub 仓库（origin：%s，github 远程：%s），跳过构建查询\n' "${PUSH_URL}" "${GH_URL:-未配置}"
  exit 0
fi

WORKFLOW_URL="https://github.com/${REPO_PATH}/actions/workflows/docker-publish.yml"

# 推送成功后不再发起网络查询：镜像尚未到达、认证失败或网络挂起都不应
# 阻塞发版命令，也不能把同名旧 tag 的构建当成本次发布。
TAG_SHA="$(git rev-parse HEAD)"
printf 'Docker Publish 构建页面：%s\n' "${WORKFLOW_URL}"
printf '本次发布：tag=%s，commit=%s；尚未确认镜像同步或构建启动\n' "${TAG_NAME}" "${TAG_SHA}"
printf '镜像长时间未同步时检查 Forgejo 推送镜像的 last_error；不要重新发版\n'
printf '安装并登录 gh 后，按本次提交与 tag 查询构建：\n'
printf "gh run list --repo %q --workflow docker-publish.yml --commit %q --branch %q --json url,headSha,headBranch,status,conclusion\n" "${REPO_PATH}" "${TAG_SHA}" "${TAG_NAME}"

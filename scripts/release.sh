#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

VERSION_FILE="${ROOT_DIR}/VERSION"
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'

err() {
  printf '错误：%s\n' "$1" >&2
  exit 1
}

[[ $# -le 1 ]] || err "用法：$0 [version]，例如 $0 0.2.0"

# 前置检查：本地检查在前，网络检查在后
BRANCH="$(git branch --show-current)"
[[ "${BRANCH}" == "main" ]] || err "当前分支不是 main（当前：${BRANCH:-detached HEAD}），请切换到 main 再发版"

[[ -z "$(git status --porcelain)" ]] || err "工作区有未提交的改动，请先提交或清理"

# 目标版本：参数优先（允许误写前导 v），否则取 VERSION 补丁号 +1
[[ -f "${VERSION_FILE}" ]] || err "VERSION 文件不存在"
CURRENT="$(cat "${VERSION_FILE}")"
[[ "${CURRENT}" =~ ${SEMVER_RE} ]] || err "VERSION 文件内容不是合法版本号：${CURRENT}"

if [[ -n "${1:-}" ]]; then
  VERSION="${1#v}"
  [[ "${VERSION}" =~ ${SEMVER_RE} ]] || err "无效的版本号：$1（应为 X.Y.Z）"
else
  IFS='.' read -r V_MAJOR V_MINOR V_PATCH <<< "${CURRENT}"
  VERSION="${V_MAJOR}.${V_MINOR}.$((V_PATCH + 1))"
fi
TAG_NAME="v${VERSION}"

git fetch origin
git rev-parse -q --verify origin/main >/dev/null || err "远程不存在 origin/main"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || err "本地 HEAD 与 origin/main 不一致，请先 git pull 同步"

if git rev-parse -q --verify "refs/tags/${TAG_NAME}" >/dev/null; then
  err "tag ${TAG_NAME} 本地已存在"
fi
REMOTE_TAG="$(git ls-remote --tags origin "refs/tags/${TAG_NAME}")" || err "无法查询远程 tag，请检查网络与 origin 配置"
[[ -z "${REMOTE_TAG}" ]] || err "tag ${TAG_NAME} 远程已存在"

printf '发版：%s -> %s\n' "${CURRENT}" "${VERSION}"

printf '%s\n' "${VERSION}" > "${VERSION_FILE}"
git add VERSION
git commit -m "chore: bump version to ${VERSION}"
git tag "${TAG_NAME}"
git push origin main
git push origin "${TAG_NAME}"

printf '已推送 main 与 tag %s\n' "${TAG_NAME}"

# 推送后打印 Docker Publish 的 run 链接，不等待构建完成
# 用 git config 读配置的 URL：git remote get-url 会展开 url.insteadOf 重写，拿到的是传输地址
REMOTE_URL="$(git config --get remote.origin.url)"
REPO_PATH=""
case "${REMOTE_URL}" in
  https://github.com/*)   REPO_PATH="${REMOTE_URL#https://github.com/}" ;;
  http://github.com/*)    REPO_PATH="${REMOTE_URL#http://github.com/}" ;;
  git@github.com:*)       REPO_PATH="${REMOTE_URL#git@github.com:}" ;;
  ssh://git@github.com/*) REPO_PATH="${REMOTE_URL#ssh://git@github.com/}" ;;
esac
REPO_PATH="${REPO_PATH%.git}"
REPO_PATH="${REPO_PATH%/}"

if [[ -z "${REPO_PATH}" ]]; then
  printf '远程地址不是 github.com，跳过构建查询\n'
  exit 0
fi

WORKFLOW_URL="https://github.com/${REPO_PATH}/actions/workflows/docker-publish.yml"

if ! command -v gh >/dev/null 2>&1; then
  printf '未安装 gh，构建页面：%s\n' "${WORKFLOW_URL}"
  printf '安装 gh 后可用 gh run watch 跟进构建\n'
  exit 0
fi

TAG_SHA="$(git rev-parse HEAD)"
RUN_URL=""
for _ in {1..12}; do
  RUN_URL="$(gh run list --repo "${REPO_PATH}" --workflow=docker-publish.yml \
    --json url,headSha,headBranch -L 20 \
    --jq ".[] | select(.headSha == \"${TAG_SHA}\" or .headBranch == \"${TAG_NAME}\") | .url" 2>/dev/null | head -n 1 || true)"
  [[ -n "${RUN_URL}" ]] && break
  sleep 5
done

if [[ -n "${RUN_URL}" ]]; then
  printf 'Docker Publish run：%s\n' "${RUN_URL}"
else
  printf '暂未查到 Docker Publish run，构建页面：%s\n' "${WORKFLOW_URL}"
fi
printf '如需等待构建完成，运行 gh run watch\n'

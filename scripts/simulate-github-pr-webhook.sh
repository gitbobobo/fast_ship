#!/usr/bin/env bash
# 模拟 GitHub pull_request webhook（INT-93 自测）。
# 以 openssl 生成 X-Hub-Signature-256，POST /api/github/webhooks。
# payload 字段严格用 GitHub 官方 pull_request 事件形状。
#
# 用法：
#   scripts/simulate-github-pr-webhook.sh [options]
# 选项：
#   --url URL            服务基址（默认 http://localhost:4888）
#   --secret SECRET      webhook 验签密钥（默认 change-me-webhook-secret）
#   --event ACTION       opened | synchronize（默认 opened）
#   --repo OWNER/NAME    仓库（默认 octocat/hello-world）
#   --pr NUMBER          PR 号（默认 1）
#   --sha SHA            head commit SHA（默认随机生成）
#   --delivery ID        X-GitHub-Delivery（默认随机 UUID，重复投递测幂等可固定）
#   --draft              pull_request.draft=true
#   --fork               head.repo.fork=true
#   --title TITLE        PR 标题
set -euo pipefail

BASE_URL="http://localhost:4888"
SECRET="change-me-webhook-secret"
ACTION="opened"
REPO="octocat/hello-world"
PR_NUMBER=1
SHA="$(uuidgen | tr -d '-')"
DELIVERY="$(uuidgen | tr 'A-Z' 'a-z')"
DRAFT=false
FORK=false
TITLE="Simulated PR"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --url) BASE_URL="$2"; shift 2 ;;
    --secret) SECRET="$2"; shift 2 ;;
    --event) ACTION="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    --pr) PR_NUMBER="$2"; shift 2 ;;
    --sha) SHA="$2"; shift 2 ;;
    --delivery) DELIVERY="$2"; shift 2 ;;
    --draft) DRAFT=true; shift ;;
    --fork) FORK=true; shift ;;
    --title) TITLE="$2"; shift 2 ;;
    *) echo "未知选项: $1" >&2; exit 2 ;;
  esac
done

OWNER="${REPO%%/*}"
NAME="${REPO#*/}"
if [[ -z "$OWNER" || -z "$NAME" || "$NAME" == "$REPO" ]]; then
  echo "仓库须为 owner/name 形式：$REPO" >&2
  exit 2
fi

# 写 UTF-8 无 BOM 的 payload 文件再引用（不经 stdin 管道、不内联）。
PAYLOAD_FILE="$(mktemp -t fast_ship_pr_payload.XXXXXX.json)"
trap 'rm -f "$PAYLOAD_FILE"' EXIT
cat > "$PAYLOAD_FILE" <<EOF
{
  "action": "${ACTION}",
  "number": ${PR_NUMBER},
  "pull_request": {
    "draft": ${DRAFT},
    "html_url": "https://github.com/${REPO}/pull/${PR_NUMBER}",
    "title": "${TITLE}",
    "body": "simulated pull request for INT-93 self-test",
    "head": {
      "sha": "${SHA}",
      "ref": "feature-branch",
      "repo": { "fork": ${FORK}, "full_name": "${REPO}" }
    },
    "base": {
      "ref": "main",
      "repo": { "full_name": "${REPO}" }
    }
  },
  "repository": {
    "full_name": "${REPO}",
    "owner": { "login": "${OWNER}" },
    "name": "${NAME}"
  },
  "installation": { "id": 12345 }
}
EOF

SIGNATURE="sha256=$(openssl dgst -sha256 -hmac "$SECRET" < "$PAYLOAD_FILE" | awk '{print $NF}')"

echo "POST ${BASE_URL}/api/github/webhooks  event=pull_request action=${ACTION} repo=${REPO}#${PR_NUMBER} sha=${SHA} delivery=${DELIVERY}"
curl -sS -o /tmp/fast_ship_webhook_resp.txt -w "HTTP %{http_code}\n" \
  -X POST "${BASE_URL}/api/github/webhooks" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-GitHub-Delivery: ${DELIVERY}" \
  -H "X-Hub-Signature-256: ${SIGNATURE}" \
  --data-binary "@${PAYLOAD_FILE}"

RESP="$(cat /tmp/fast_ship_webhook_resp.txt 2>/dev/null || true)"
if [[ -n "$RESP" ]]; then
  echo "响应体: ${RESP}"
fi

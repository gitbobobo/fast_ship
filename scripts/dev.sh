#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-all}"

SERVER_PID=""
WEB_PID=""

cleanup() {
  local exit_code=$?

  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi

  if [[ -n "${WEB_PID}" ]] && kill -0 "${WEB_PID}" 2>/dev/null; then
    kill "${WEB_PID}" 2>/dev/null || true
  fi

  wait "${SERVER_PID:-}" 2>/dev/null || true
  wait "${WEB_PID:-}" 2>/dev/null || true

  exit "${exit_code}"
}

run_server() {
  cd "${ROOT_DIR}/server"
  exec make dev
}

run_web() {
  cd "${ROOT_DIR}/web"
  exec pnpm dev
}

run_both() {
  trap cleanup INT TERM EXIT

  run_server &
  SERVER_PID=$!

  run_web &
  WEB_PID=$!

  printf "server pid: %s\n" "${SERVER_PID}"
  printf "web pid: %s\n" "${WEB_PID}"

  while true; do
    if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
      wait "${SERVER_PID}"
      return $?
    fi

    if ! kill -0 "${WEB_PID}" 2>/dev/null; then
      wait "${WEB_PID}"
      return $?
    fi

    sleep 1
  done
}

case "${MODE}" in
  all)
    run_both
    ;;
  server)
    run_server
    ;;
  web)
    run_web
    ;;
  *)
    printf "Usage: %s [all|server|web]\n" "$0" >&2
    exit 1
    ;;
esac

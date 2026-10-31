#!/usr/bin/env bash
# 在隔离的真实 git 裸仓库运行 release.sh；所有 GitHub URL 都改写到本地。
# 推送后故意让网络查询/gh 失败，验证脚本不调用它们、不误报构建已启动。
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REAL_GIT="$(command -v git)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/fast-ship-release-fixture.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
PASS=0
GH_URL="https://github.com/test-owner/test-repo.git"
FJ_URL="https://forgejo.example/fjbobo/test-repo.git"

equal() {
  if [[ "$1" != "$2" ]]; then
    printf 'FAIL %s: actual=%q expected=%q\n' "$3" "$1" "$2" >&2
    exit 1
  fi
  PASS=$((PASS + 1))
}
contains() {
  if ! grep -qF -- "$2" <<< "$1"; then
    printf 'FAIL %s: missing %q\n' "$3" "$2" >&2
    exit 1
  fi
  PASS=$((PASS + 1))
}
setup() {
  local name="$1" url="$2"
  SC="${WORK}/${name}"
  REPO="${SC}/repo"
  BARE="${SC}/origin.git"
  mkdir -p "${SC}/bin" "${REPO}/scripts"
  git init --bare -q -b main "${BARE}"
  git init -q -b main "${REPO}"
  git -C "${REPO}" config user.email fixture@example.invalid
  git -C "${REPO}" config user.name fixture
  # 禁用开发机签名/钩子，避免测试向外访问或请求系统权限。
  git -C "${REPO}" config commit.gpgsign false
  git -C "${REPO}" config tag.gpgsign false
  git -C "${REPO}" config core.hooksPath "${SC}/no-hooks"
  cp "${ROOT_DIR}/scripts/release.sh" "${REPO}/scripts/release.sh"
  printf '0.1.67\n' > "${REPO}/VERSION"
  git -C "${REPO}" add .
  git -C "${REPO}" commit -qm init
  BEFORE="$(git -C "${REPO}" rev-parse HEAD)"
  git -C "${REPO}" remote add origin "${BARE}"
  git -C "${REPO}" push -q origin main
  git -C "${REPO}" remote set-url origin "${url}"
  git -C "${REPO}" config url."${BARE}".insteadOf "${url}"
  cat > "${SC}/bin/git" <<EOF
#!/usr/bin/env bash
if [[ -f "${SC}/pushed" && "\$1" == "ls-remote" ]]; then
  echo unexpected-network >> "${SC}/unexpected"
  exit 89
fi
"${REAL_GIT}" "\$@"
rc=\$?
if [[ "\$1" == push && "\$2" == --atomic && \$rc == 0 ]]; then
  touch "${SC}/pushed"
fi
exit \$rc
EOF
  cat > "${SC}/bin/gh" <<EOF
#!/usr/bin/env bash
echo unexpected-gh >> "${SC}/unexpected"
exit 77
EOF
  chmod +x "${SC}/bin/git" "${SC}/bin/gh"
}
run_release() {
  RC=0
  (cd "${REPO}" && PATH="${SC}/bin:${PATH}" GIT_EDITOR=false bash scripts/release.sh) > "${SC}/out.log" 2> "${SC}/err.log" || RC=$?
  OUT="$(cat "${SC}/out.log")"
  ERR_OUT="$(cat "${SC}/err.log")"
}
verify_pushed() {
  local sha
  sha="$(git -C "${REPO}" rev-parse HEAD)"
  equal "${RC}" 0 "推送成功退出0"
  equal "$(cat "${REPO}/VERSION")" 0.1.68 "VERSION保留"
  equal "$(git --git-dir="${BARE}" rev-parse main)" "${sha}" "main已到真实远端"
  equal "$(git --git-dir="${BARE}" rev-parse refs/tags/v0.1.68)" "${sha}" "tag已到真实远端"
  equal "$(git -C "${REPO}" rev-parse refs/tags/v0.1.68)" "${sha}" "本地tag保留"
  equal "$(git -C "${REPO}" status --porcelain)" "" "工作区干净"
  equal "$([[ -f "${SC}/unexpected" ]] && cat "${SC}/unexpected" || true)" "" "推送后无网络或gh调用"
}

verify_unchanged() {
  [[ "${RC}" -ne 0 ]] || { echo 'FAIL 非法目标必须提前拒绝'; exit 1; }
  PASS=$((PASS + 1))
  equal "$(git -C "${REPO}" rev-parse HEAD)" "${BEFORE}" "预检拒绝后HEAD未变"
  equal "$(cat "${REPO}/VERSION")" 0.1.67 "预检拒绝后VERSION未变"
  equal "$(git -C "${REPO}" tag -l v0.1.68)" "" "预检拒绝后无本地tag"
  equal "$(git --git-dir="${BARE}" rev-parse main)" "${BEFORE}" "预检拒绝后源端未变"
  equal "$(git --git-dir="${BARE}" tag -l v0.1.68)" "" "预检拒绝后源端无tag"
  equal "$(git --git-dir="${SECOND}" rev-parse main)" "${BEFORE}" "预检拒绝后第二接收端未变"
  equal "$(git --git-dir="${SECOND}" tag -l v0.1.68)" "" "预检拒绝后第二接收端无tag"
}

printf '== 多pushurl在任一远端接收前拒绝 ==\n'
setup multi-pushurl "${FJ_URL}"
SECOND="${SC}/second.git"
git clone -q --bare "${BARE}" "${SECOND}"
cat > "${SECOND}/hooks/pre-receive" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "${SECOND}/hooks/pre-receive"
git -C "${REPO}" config --add remote.origin.pushurl "${BARE}"
git -C "${REPO}" config --add remote.origin.pushurl "${SECOND}"
run_release
verify_unchanged
contains "${ERR_OUT}" "只接受一个 origin 推送目标" "多pushurl明确报错"

printf '== 无pushurl的多个url同样拒绝 ==\n'
setup multi-url "${FJ_URL}"
SECOND="${SC}/second.git"
git clone -q --bare "${BARE}" "${SECOND}"
git -C "${REPO}" config --add remote.origin.url "${SECOND}"
run_release
verify_unchanged

printf '== 单pushurl与pushInsteadOf按Git实际目标预检 ==\n'
setup single-pushurl "${FJ_URL}"
git -C "${REPO}" config remote.origin.pushurl "${BARE}"
run_release
verify_pushed

setup push-rewrite "${FJ_URL}"
git -C "${REPO}" config --unset-all url."${BARE}".insteadOf
git -C "${REPO}" config url."${BARE}".pushInsteadOf "${FJ_URL}"
run_release
verify_pushed

setup explicit-pushurl-wins "${FJ_URL}"
git -C "${REPO}" config url."${SC}/must-not-receive.git".pushInsteadOf "${FJ_URL}"
git -C "${REPO}" config remote.origin.pushurl "${BARE}"
run_release
verify_pushed

printf '== 层叠重写在写入前拒绝 ==\n'
setup nested-rewrite "${FJ_URL}"
SECOND="${SC}/second.git"
git clone -q --bare "${BARE}" "${SECOND}"
git -C "${REPO}" config url."${SECOND}".insteadOf "${BARE}"
run_release
verify_unchanged
contains "${ERR_OUT}" "层叠 URL 重写" "拒绝预检会再次改变目标的重写"

printf '== GitHub origin ==\n'
setup github "${GH_URL}"
run_release
verify_pushed
contains "${OUT}" "Docker Publish 构建页面：https://github.com/test-owner/test-repo/actions/workflows/docker-publish.yml" "构建页"
contains "${OUT}" "--commit $(git -C "${REPO}" rev-parse HEAD) --branch v0.1.68" "查询同时限定commit和tag"
contains "${OUT}" "尚未确认镜像同步或构建启动" "不把推送成功当作构建成功"

printf '== Forgejo origin + disabled GitHub push ==\n'
setup forgejo "${FJ_URL}"
git -C "${REPO}" remote add github "${GH_URL}"
git -C "${REPO}" remote set-url --push github disabled://github-read-only
run_release
verify_pushed
contains "${OUT}" "https://github.com/test-owner/test-repo/actions/workflows/docker-publish.yml" "使用GitHub fetch URL"
equal "$(git -C "${REPO}" remote get-url --push github)" disabled://github-read-only "github仍禁推"

printf '== 新克隆没有github remote ==\n'
setup no-mirror "${FJ_URL}"
run_release
verify_pushed
contains "${OUT}" "跳过构建查询" "无镜像配置安全退出"

printf '== URL userinfo 不进入构建页面或输出 ==\n'
MARKER='fixture-userinfo-marker'
setup userinfo-origin "https://oauth2:${MARKER}@github.com/test-owner/test-repo.git"
run_release
verify_pushed
contains "${OUT}" "https://github.com/test-owner/test-repo/actions/workflows/docker-publish.yml" "GitHub origin剥除userinfo"
equal "$([[ "${OUT}${ERR_OUT}" == *"${MARKER}"* ]] && echo leaked || true)" "" "GitHub凭据不回显"

setup userinfo-mirror "https://x:${MARKER}@forgejo.example/fjbobo/test-repo.git"
git -C "${REPO}" remote add github "https://oauth2:${MARKER}@github.com/test-owner/test-repo.git"
git -C "${REPO}" remote set-url --push github disabled://github-read-only
run_release
verify_pushed
contains "${OUT}" "https://github.com/test-owner/test-repo/actions/workflows/docker-publish.yml" "GitHub mirror剥除userinfo"
equal "$([[ "${OUT}${ERR_OUT}" == *"${MARKER}"* ]] && echo leaked || true)" "" "镜像凭据不回显"

setup userinfo-fallback "https://x:${MARKER}@forgejo.example/fjbobo/test-repo.git"
run_release
verify_pushed
contains "${OUT}" "跳过构建查询" "解析失败仍安全退出"
equal "$([[ "${OUT}${ERR_OUT}" == *"${MARKER}"* ]] && echo leaked || true)" "" "兜底不回显原始URL"

printf '== 全局标签签名开启也不调用签名器 ==\n'
setup signing "${FJ_URL}"
git -C "${REPO}" config --unset tag.gpgsign
cat > "${SC}/bin/signing-must-not-run" <<EOF
#!/usr/bin/env bash
echo unexpected-signing >> "${SC}/unexpected"
exit 78
EOF
chmod +x "${SC}/bin/signing-must-not-run"
# 用隔离的全局配置开启签名并指定阻断签名器；由 release.sh 自己覆盖签名设置。
git config --file "${SC}/global.gitconfig" tag.gpgsign true
git config --file "${SC}/global.gitconfig" gpg.program "${SC}/bin/signing-must-not-run"
equal "$(GIT_CONFIG_GLOBAL="${SC}/global.gitconfig" git -C "${REPO}" config --get tag.gpgsign)" true "被测环境确实继承全局标签签名"
GIT_CONFIG_GLOBAL="${SC}/global.gitconfig" run_release
verify_pushed
equal "$(git -C "${REPO}" cat-file -t refs/tags/v0.1.68)" commit "测试仍生成轻量tag"

printf '== 原子推送被拒，回滚本地 ==\n'
setup rejected "${FJ_URL}"
cat > "${BARE}/hooks/pre-receive" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "${BARE}/hooks/pre-receive"
run_release
[[ "${RC}" -ne 0 ]] || { echo 'FAIL 拒收必须失败'; exit 1; }
PASS=$((PASS + 1))
equal "$(git -C "${REPO}" rev-parse HEAD)" "${BEFORE}" "拒收后HEAD还原"
equal "$(cat "${REPO}/VERSION")" 0.1.67 "拒收后VERSION还原"
equal "$(git --git-dir="${BARE}" rev-parse main)" "${BEFORE}" "拒收后远端main未变"
equal "$(git -C "${REPO}" tag -l v0.1.68)" "" "拒收后本地tag删除"
equal "$(git --git-dir="${BARE}" tag -l v0.1.68)" "" "拒收后远端无tag"
printf '通过 %s，失败 0\n' "${PASS}"

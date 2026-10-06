---
name: verify-fast-ship
description: Launch and drive the Fast Ship web UI end to end (Go server + React SPA) to prove user-visible behavior with screenshot and database evidence. Use when a change needs real UI verification — auth, projects, issues, versions, documents, logs, settings — on an isolated port and database that never touches other running fast_ship instances or the shared dev ports 4888/4999.
---

# Verify Fast Ship

Fast Ship is a Go (gin + sqlite) API server plus a React 19/Vite SPA. This skill starts an isolated instance from this worktree, drives the real UI headlessly, captures proof, and tears down only what it started.

All scratch and evidence live under a private per-run directory `mktemp` creates for you. The directory name is unguessable and owned exclusively by this run, so parallel runs can never share a binary, database, PID file, or evidence:

- `evidence/` — screenshots, ARIA snapshots, API read-backs, log tails. Never deleted by cleanup. **Credentials (API keys, tokens, passwords) must never land here** — not in `.png`, not in `.aria.txt`, not in `run.txt`.
- `scratch/` — built binary, sqlite db, uploads, pid/state files, secrets. Private to the run; may be deleted after the run.

```bash
mkdir -p /tmp/fast-ship-verify
V="$(mktemp -d /tmp/fast-ship-verify/run-XXXXXXXX)"   # atomic, exclusive to this run
mkdir -p "$V/evidence" "$V/scratch"
REPO="$(git rev-parse --show-toplevel)"   # this worktree
PORT=5088                                  # see Launch step 1
```

## Launch

Do not use `make dev` for verification. It binds fixed ports (server 4888 via `configs/config.yaml`, web 4999) and vite's `/api` proxy is hardcoded to `http://localhost:4888` in `web/vite.config.ts`, so a second instance cannot run side by side with anyone else's `make dev`. The isolated equivalent is: build the SPA once, then let the Go server host it via `server.web_dist_dir` on your own port with your own database.

1. **Pick a free port.** `lsof -nP -iTCP:5088 -sTCP:LISTEN` must print nothing. If it does, pick another (5089, 5090, ...) and set `PORT` accordingly. Never reuse 4888 or 4999 — those belong to other instances, not to you.

2. **Build the SPA every run.** A stale `web/dist` is the most common false-green; mtime comparisons lie, so just rebuild:

   ```bash
   # one-time, only if web/node_modules is missing:
   pnpm --dir "$REPO/web" install --frozen-lockfile
   pnpm --dir "$REPO/web" build        # produces web/dist
   ```

3. **Build the server binary into scratch.** Run the binary directly — not `go run` or `make dev-server`, which spawn a child process; the PID you record must be the process that owns the port:

   ```bash
   (cd "$REPO/server" && go build -o "$V/scratch/fast_ship" ./cmd/server)
   ```

4. **Start it**, logging outside the worktree:

   ```bash
   cd "$REPO/server"
   CONFIG_PATH="$REPO/server/configs/config.yaml" \
   FAST_SHIP_SERVER_PORT=$PORT \
   FAST_SHIP_SERVER_MODE=release \
   FAST_SHIP_DATABASE_PATH="$V/scratch/data/fast_ship.db" \
   FAST_SHIP_UPLOAD_STORAGE_PATH="$V/scratch/data/uploads" \
   FAST_SHIP_WEB_DIST_DIR="$REPO/web/dist" \
   FAST_SHIP_ISSUES_AUTO_SYNC_ENABLED=false \
   nohup "$V/scratch/fast_ship" > "$V/server.log" 2>&1 &
   echo $! > "$V/scratch/server.pid"
   ```

   `FAST_SHIP_*` env vars override `config.yaml` keys (`server.port` → `FAST_SHIP_SERVER_PORT`, etc.). `auto_sync_enabled=false` keeps a fresh instance from reaching out to GitHub.

5. **Wait for ready** (poll, no fixed sleeps, bounded curl):

   ```bash
   for i in $(seq 1 30); do
     curl -sf --connect-timeout 2 --max-time 5 "http://127.0.0.1:$PORT/" -o "$V/scratch/index.html" && break || sleep 1
   done
   grep -q 'id="root"' "$V/scratch/index.html" \
     && test "$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 2 --max-time 5 "http://127.0.0.1:$PORT/api/auth/me")" = "401" \
     && echo "READY"
   ```

   `GET /` returning the SPA HTML proves static hosting; `/api/auth/me` returning 401 proves the API and auth middleware are up. The server log prints a zap line `"msg":"服务启动","addr":":<port>"` (in release mode Gin itself is quiet — do not wait for `Listening and serving HTTP` or `[GIN-debug]` lines, they never come).

One process, one port. Teardown is in Cleanup.

## Doctor

Run this block before driving and whenever anything looks off. It is a gate, not a report — every check asserts, and the block exits non-zero on the first failure. Stop and fix before driving; do not `|| true` past it:

```bash
fail() { echo "DOCTOR FAIL: $*" >&2; exit 1; }

PID="$(cat "$V/scratch/server.pid" 2>/dev/null)" || fail "no server.pid for this run"
[ -n "$PID" ] || fail "server.pid is empty"

# The recorded PID must be our binary — same pid could now be a recycled,
# unrelated process or (worse) another worktree's instance.
[ "$(ps -p "$PID" -o comm= 2>/dev/null | xargs)" = "$V/scratch/fast_ship" ] \
  || fail "pid $PID is not $V/scratch/fast_ship (server died or pid was recycled)"

[ "$(lsof -nP -iTCP:$PORT -sTCP:LISTEN -t | head -1)" = "$PID" ] \
  || fail "port $PORT is not owned by pid $PID — another instance holds it, do not drive"

code="$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 3 --max-time 5 \
  "http://127.0.0.1:$PORT/api/auth/me")"
[ "$code" = "401" ] || fail "auth probe returned $code, expected 401"

served="$(curl -s --connect-timeout 3 --max-time 5 "http://127.0.0.1:$PORT/" \
  | grep -oE 'assets/index-[^"]+\.js' | head -1)"
dist="$(grep -oE 'assets/index-[^"]+\.js' "$REPO/web/dist/index.html" | head -1)"
[ -n "$served" ] && [ "$served" = "$dist" ] \
  || fail "bundle mismatch: served=$served dist=$dist — web/dist is stale or FAST_SHIP_WEB_DIST_DIR points elsewhere; rebuild"

grep -q '服务启动' "$V/server.log" || fail "no 服务启动 in server.log — check tail for panic/migration errors"
tail -20 "$V/server.log"
echo "DOCTOR OK pid=$PID port=$PORT bundle=$served"
```

Failure readings: process dead → `server.log` tail has the panic/migration error; port owned by a different PID → you are looking at someone else's instance, do not drive it; `000`/timeout on the probe → server hung or port wrong; bundle mismatch → rebuild `web/dist`.

Login-state check is part of Doctor only after a session exists: drive `shot /dashboard` and confirm the page renders the app (sidebar links 仪表盘/项目/问题) rather than redirecting to `/login`. A redirect means the stored JWT expired or the user was deleted — run `login` again.

## Drive

Harness: headless Chromium via Python Playwright (`import playwright` must work; one-time setup `python3 -m playwright install chromium`, installs to `~/Library/Caches`, not the repo; if the bundled browser executable is absent, fsdrive falls back to the system Google Chrome — still headless). All interaction goes through `scripts/fsdrive.py`:

```bash
DRV="python3 $REPO/skills/verify-fast-ship/scripts/fsdrive.py --base http://127.0.0.1:$PORT --evidence $V/evidence --state $V/scratch/state.json"

$DRV register verifybot verifybot@example.com VerifyPass1   # fills the real /register form, lands on /dashboard, saves session
$DRV login verifybot VerifyPass1                            # same through /login
$DRV shot /projects 01-projects                             # screenshot + ARIA snapshot of any route

# scripted flow — write the scenario into scratch, then run it:
cat > "$V/scratch/scenario.py" <<'PYEOF'
register_ui("verifybot", "verifybot@example.com", "VerifyPass1")
shot("01-dashboard")
PYEOF
$DRV run "$V/scratch/scenario.py"                           # helpers injected (see Helpers)
```

Omit `--state` (or point it at a nonexistent file) when you need an unauthenticated context — e.g. route-guard checks.

Selector conventions (from this codebase, in preference order):

- **Route paths** are stable: `/login`, `/register`, `/dashboard`, `/projects`, `/issues`, `/issues?project=<uuid>`, `/projects/<uuid>/issues/new`, `/projects/<pid>/issues/<iid>`, `/versions`, `/documents`, `/logs`, `/board`, `/settings/api-keys`.
- **Roles + accessible names**: sidebar `get_by_role("link", name="项目")`; primary buttons `get_by_role("button", name="创建项目" / "创建 API Key" / "发布评论")`; menu items `get_by_role("menuitem", name="登出")`; dialogs `get_by_role("dialog")`. On `/issues`, `新建问题` is a **link** (`render={<Link/>}`), not a button: `get_by_role("link", name="新建问题")`.
- **Comboboxes** (Base UI `Select` triggers carry `role="combobox"` — a nameFrom:author role, so the displayed text is NOT an accessible name and `name=` never matches; select the trigger by its shown value with `filter(has_text=…)`): project selector `page.get_by_role("combobox").filter(has_text="验证项目")`; state filter `…has_text="开启"` (options `全部状态`/`开启`/`关闭`); source `…"来源"` (`全部来源`/`内部`/`GitHub`); internal status `…"内部状态"`; sort `…"最近更新"`. Options inside the open listbox DO take their name from content — pick with `page.get_by_role("option", name="关闭")`.
- **Labels** (real `<Label htmlFor>` pairs): register `用户名`/`邮箱`/`密码`; login `用户名或邮箱`/`密码`; project dialog `项目名称`/`项目描述（可选）`; API key dialog `备注名称`; issue form `标题`/`描述`/`内部状态`.
- **IDs/testids** where present: `#issue-title`, `#issue-body` (the MDEditor textarea), `data-testid="issue-form-submit"` (the header submit button — rendered once, `hideSubmitButton` suppresses the in-form copy), `aria-label="编辑标题"`. `#issue-workflow-status` exists only on the **new/edit form**; on the detail page the workflow select is the `combobox` next to the `内部状态` label in the 元数据 card — scope it with `page.get_by_text("内部状态", exact=True).locator("..").get_by_role("combobox")`.
- On the issue detail page the inline title editor's submit button is a plain `保存` text button with **no** aria-label — `get_by_role("button", name="保存", exact=True)` while editing (the checklist's `aria-label="保存"` icon is a different control and only exists in checklist-edit mode).
- **Placeholders** for inputs without labels: `搜索项目名、描述或仓库`, `搜索标题、编号或作者`, `使用 Markdown 输入评论内容`.
- Never use coordinates or tab order. Never use `page.keyboard.press("Tab")` to navigate.

End-to-end example (a scenario file for `$DRV run` — register, create a project through the dialog, verify the card, read the row back from sqlite):

```python
# scenario.py — write to "$V/scratch/scenario.py", then: $DRV run "$V/scratch/scenario.py"
register_ui("verifybot", "verifybot@example.com", "VerifyPass1")
shot("01-dashboard")

page.get_by_role("link", name="项目").click()
page.wait_for_url("**/projects")
page.get_by_role("button", name="创建项目").click()
page.get_by_label("项目名称").fill("验证项目")
page.get_by_role("dialog").get_by_role("button", name="创建项目").click()
page.wait_for_url("**/issues?project=*")   # create auto-navigates to the new project's issues
shot("02-project-created")

status, body = api_get("/api/projects")
assert status == 200, body
assert any(p["name"] == "验证项目" for p in body["data"]["items"]), body
```

Note the two `创建项目` buttons (header opens the dialog, dialog footer submits) — scope with `get_by_role("dialog").get_by_role("button", ...)`.

## Evidence

Everything under `$V/evidence/` survives cleanup — this directory is the deliverable.

- **Proof standard**: drive the real user path (browser forms/buttons). API calls are for read-back of side effects only, never for fabricating the state under test.
- **Action + result**: capture the state that shows the action (filled form, open dialog) and the state that proves the result (new card/row/heading), e.g. `01-projects-empty.png` → `02-create-dialog.png` → `03-project-created.png`. `shot()` writes a `.png` and a matching `.aria.txt`; keep both.
- **Credentials never enter evidence.** API keys, tokens, and passwords must not appear in `.png`, `.aria.txt`, logs, or `run.txt`. Store them with `save_secret("name", value)` → `$V/scratch/` (mode 0600). When a credential is only visible in a transient dialog, screenshot **after** the dialog closes (e.g. API-key reveal → shot the closed list, which shows only the `key_prefix` + `•••`).
- **Side effects**: after a UI mutation, verify the stored truth — `sqlite3 "$V/scratch/data/fast_ship.db" "select title, source from issues;" > "$V/evidence/db-issues.txt"`, or `api_get("/api/projects")` from a scenario.
- **Server log**: `tail -50 "$V/server.log" > "$V/evidence/server-tail.log"` before teardown.
- Record the run id, port, and base URL in `evidence/run.txt` so artifacts are attributable.

## Cleanup

Kill only the PID you recorded, and only after confirming it still is your server: `server.pid` could point at a recycled or foreign process, and other fast_ship instances on this machine are not yours. If identity does not match, stop and report — do not kill anything.

```bash
PID="$(cat "$V/scratch/server.pid" 2>/dev/null || true)"
if [ -n "$PID" ] && [ "$(ps -p "$PID" -o comm= 2>/dev/null | xargs)" = "$V/scratch/fast_ship" ]; then
  kill "$PID"
else
  echo "recorded pid ${PID:-none} is not our server binary — leaving it alone" >&2
fi
for i in $(seq 1 10); do lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null || break; sleep 1; done
lsof -nP -iTCP:$PORT -sTCP:LISTEN && echo "WARN: port still held — identify the owner before retrying" || echo "port free"
ls "$V/evidence"      # proof must still be here after teardown
```

The headless browser exits when each `fsdrive.py` invocation finishes — nothing to kill. `scratch/` (binary, db, uploads, state, secrets) may be removed once you have your evidence; `evidence/` must remain. If a run fails partway, still run this block — a failed attempt is not a reason to strand a server.

## Helpers

`scripts/fsdrive.py` (this directory) — see Drive for usage. Scenario files passed to `run` are plain Python executed with `page`, `shot(name)`, `register_ui(u,e,p)`, `login_ui(l,p)`, `jwt()`, `api_get(path)`, `save_secret(name, value)`, `persist_state()`, `BASE`, `EVIDENCE`, `SCRATCH` pre-bound. `api_get` attaches the JWT from the browser's `localStorage` (`fast-ship-auth`) and applies the `--timeout` budget — use it for read-backs after UI actions. `save_secret` writes credentials into the scratch dir with mode 0600 — the only sanctioned place for them.

The feature map in `features/` lists the user-visible surface and a recipe per feature; drive at least the feature your change touches.

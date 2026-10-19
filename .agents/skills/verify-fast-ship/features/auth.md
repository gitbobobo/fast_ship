# Sign up, sign in, sign out

Fast Ship uses JWT auth. Registration is **closed by default**: the first account on a fresh database can be created directly on `/register` and becomes the deployment's `admin`; every later account needs a one-time invite link from an admin (`/register?invite=<token>`). Signing up signs in immediately and lands on `/dashboard`. Sessions persist in `localStorage` (`fast-ship-auth`); every authenticated route redirects to `/login` when no token is present.

## Sub-features

- `register` creates the **first** (admin) account through `/register` and lands signed-in on `/dashboard`. Only works on a fresh database.
- `register-invite` creates a member account through `/register?invite=<token>` (get the token from 设置 → 成员 → 创建邀请, or from `POST /api/invites` with an admin session). An already-used or expired link shows `邀请链接无效或已过期`.
- `register-closed` submitting `/register` without an invite on a deployment that already has users surfaces the backend message `公开注册已关闭，请联系管理员获取邀请链接` (HTTP 403 / 40306).
- `members-admin` (设置 → 成员, admin only) creates invites, shows the one-time link, and changes roles.
- `register-validation` shows inline field errors for bad input (short username, weak password).
- `login` signs in through `/login` with username or email.
- `route-guard` redirects unauthenticated visitors from any app route to `/login`.
- `session-persist` keeps the session across a full page reload.
- `logout` signs out from the user menu and returns to `/login`.

## How to get to it (user POV)

- Open `/register` directly, or follow the `注册` link on `/login`.
- Open `/login` directly, or get redirected there from any app route while signed out.
- Open the user menu (avatar button, bottom of the sidebar) and choose `登出`.

## Driving it with fsdrive

Preconditions:

- Fast Ship healthy at `http://127.0.0.1:$PORT`; the SKILL.md Doctor block printed `DOCTOR OK`.
- No account named `verifybot` exists (fresh database satisfies this). Registration is closed once any user exists — `$DRV register` only works against a fresh database. For a second account, create an invite with the admin session first:

  ```bash
  # inside a scenario with the admin session loaded:
  page.goto(BASE + "/settings/members")
  page.get_by_role("button", name="创建邀请").click()
  # read the one-time link from the page, then open it in a session-less driver
  ```

  The link is `…/register?invite=<token>`; hand it to `DRV_NOAUTH` with `page.goto(<link>)` before filling the form.
- `$DRV` is set per SKILL.md. For steps that must run **without** a session, use a second driver with no `--state`:
  `DRV_NOAUTH="python3 $REPO/.agents/skills/verify-fast-ship/scripts/fsdrive.py --base http://127.0.0.1:$PORT --evidence $V/evidence"`

- **Register.** Run `$DRV register verifybot verifybot@example.com VerifyPass1`. `register-form.png` shows the filled form before submit; `register-dashboard.png` shows `/dashboard` with sidebar links `仪表盘`/`项目`/`问题` and the user-menu button containing `verifybot` and `v<VERSION>`.

- **Field validation** — run with the stateless driver. Scenario:

  ```bash
  cat > "$V/scratch/validation.py" <<'PYEOF'
  page.goto(BASE + "/register")
  page.get_by_label("用户名").fill("x")
  page.get_by_label("邮箱").fill("verifybot@example.com")
  page.get_by_label("密码").fill("weak")
  page.get_by_role("button", name="注册").click()
  page.get_by_text("用户名至少 2 个字符").wait_for()
  page.get_by_text("密码至少 8 位").wait_for()          # short password hits the length rule first
  page.get_by_label("用户名").fill("verifybot")
  page.get_by_label("密码").fill("weakpass1")          # ≥8 chars, no uppercase
  page.get_by_role("button", name="注册").click()
  page.get_by_text("需包含大写字母").wait_for()
  assert page.url.endswith("/register"), page.url
  shot("register-validation")
  PYEOF
  $DRV_NOAUTH run "$V/scratch/validation.py"
  ```

- **Route guard** — also with the stateless driver (with `--state` loaded the app would just render `/projects`):

  ```bash
  cat > "$V/scratch/guard.py" <<'PYEOF'
  page.goto(BASE + "/projects")
  page.wait_for_url("**/login")
  assert page.url.endswith("/login"), page.url
  shot("guard-redirect")
  PYEOF
  $DRV_NOAUTH run "$V/scratch/guard.py"
  ```

- **Login.** Run `$DRV login verifybot VerifyPass1`. Lands on `/dashboard`; `login-form.png` shows the filled form.

- **Session persist.** With `--state` saved by register/login, `$DRV shot /dashboard persist` still shows the app, not `/login`.

- **Logout** — in a signed-in scenario (plain Python, `import re` works if needed):

  ```python
  page.get_by_role("button", name="verifybot").click()      # user-menu trigger, substring-matches "V verifybot v…"
  page.get_by_role("menuitem", name="登出").click()
  page.wait_for_url("**/login")
  shot("logged-out")
  ```

- **Proof.** `register-form.png` + `register-dashboard.png` + `persist.png` + post-logout `login` URL assertion + `guard-redirect.aria.txt` showing the login form. Side effect: `sqlite3 "$V/scratch/data/fast_ship.db" "select username, email from users;"` lists `verifybot`.

## Gotchas

- Password must be ≥8 chars with upper, lower, and digit — zod validates client-side before submit, so a weak password produces inline errors, not a server failure. The length rule fires before the character-class rules, so `weak` shows `密码至少 8 位`, not `需包含大写字母`.
- The user-menu trigger's accessible name is the avatar initial plus username plus `v<version>` (e.g. `V verifybot v0.1.43`) — match by substring with `name="verifybot"`, or `name=re.compile(username)` for an anchored match.
- Register auto-logs-in: there is no email verification or approval step. A duplicate username/email surfaces the backend message (`用户名已存在` / `邮箱已存在`); other failures show the backend message too, not a generic text.
- The first registered user is the deployment admin. Everyone else is a `member` and cannot see 设置 → 部署 / 设置 → 成员 / the GitHub App create button; hitting those URLs directly renders a `需要管理员权限` empty state.
- `/api/auth/me` answering 401 is healthy — it means the middleware works, not that the server is broken.
- localStorage is per-origin: a session saved against port 5088 does not carry to another port.
- A live session redirects `/login` and `/register` to `/dashboard` (`AuthLayout`), so driving the forms with a saved `--state` times out waiting for labels. `register_ui`/`login_ui` clear `fast-ship-auth` first; hand-rolled scenarios must do the same (goto → `localStorage.removeItem('fast-ship-auth')` → goto again) or use a no-`--state` invocation.

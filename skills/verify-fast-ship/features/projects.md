# Projects

A project is the top-level container for issues, versions, documents, and logs. Users create projects from the `/projects` page, search the card grid, and open a project's issues by clicking its card.

## Sub-features

- `project-create` opens the `新建项目` dialog and persists a named project.
- `project-list` renders created projects as cards with name, description, repo link status, and latest-version badge.
- `project-search` filters the card grid by name, description, or repo slug.
- `project-open` navigates from a card to that project's issue list (`/issues?project=<uuid>`).
- `project-edit` reopens the dialog from the card's `更多操作` menu and saves changes.
- `project-delete` removes the project through a confirmation dialog.

## How to get to it (user POV)

- Choose the `项目` link in the sidebar.
- Navigate to `/projects` directly.

## Driving it with fsdrive

Preconditions:

- Signed in. In a `run` scenario the page starts on `about:blank` — call `login_ui("verifybot", "VerifyPass1")`/`register_ui(...)` first (lands on `/dashboard`, so the sidebar exists), or `page.goto(BASE + "/projects")` to navigate directly.
- No project named `验证项目` exists.

All commands below assume a signed-in scenario (`login_ui` already called or `--state` loaded) and, for relative clicks, that the app is already on a rendered page.

- **Open list.** From any app page: `page.get_by_role("link", name="项目").click()`; or `page.goto(BASE + "/projects")`. URL `/projects`, header `项目`, and — on a fresh account — empty state `暂无项目`. `shot("projects-empty")`.
- **Create.** `page.get_by_role("button", name="创建项目").click()` opens dialog `新建项目`; `page.get_by_label("项目名称").fill("验证项目")` and `page.get_by_label("项目描述（可选）").fill("验证用")`; submit with `page.get_by_role("dialog").get_by_role("button", name="创建项目").click()`. Dialog closes, toast `项目创建成功` appears, and the app navigates to `/issues?project=<uuid>` with the project selector (`combobox`) showing `验证项目`. `shot("project-created")`.
- **Card grid.** `page.goto(BASE + "/projects")`; the grid shows a `验证项目` card.
- **Search.** `page.get_by_placeholder("搜索项目名、描述或仓库").fill("验证")` keeps `验证项目` visible; fill `zzz` shows `没有匹配的项目`.
- **Open.** On `/projects`, click the card (`page.get_by_text("验证项目").first.click()`); URL becomes `/issues?project=<uuid>` and the issue list's project selector shows `验证项目`.
- **Edit.** Back on `/projects`: `page.get_by_label("更多操作").click()` on the card → `page.get_by_role("menuitem", name="编辑").click()` → dialog `编辑项目` → `page.get_by_label("项目描述（可选）").fill("新描述")` → submit with `page.get_by_role("dialog").get_by_role("button", name="保存", exact=True).click()` (the edit-mode submit label is `保存`, not `保存修改`). Card shows the new description.
- **Delete.** `更多操作` → `menuitem 删除` → AlertDialog `确认删除项目?` → `page.get_by_role("button", name="确认删除").click()`. Card disappears; toast `项目已删除`.
- **Proof.** `project-created.png` + `api_get("/api/projects")` returning `验证项目` in `data.items`, or `sqlite3 "$V/scratch/data/fast_ship.db" "select name from projects;"` — projects does have a `name` column (issues does not; it uses `title`).

## Gotchas

- Two buttons share the name `创建项目` (header opens, dialog submits) — scope to `get_by_role("dialog")` or use `.last`.
- The card is a clickable surface, not a link — use `get_by_text(name).first.click()` or locate the card; `更多操作` already stops propagation, just click it.
- `repository_url` + missing `github_token` triggers inline validation (`请输入 GitHub Token 或选择复用已有项目的 Token`) — leave both empty for a local-only project.
- Deleting a project deletes its versions and artifacts; confirm the AlertDialog, not just the menu item.
- Search is client-side deferred (`useDeferredValue`) — wait for the grid to update, not a fixed sleep.
- `get_by_label("项目描述")` substring-matches `项目描述（可选）`; use the full label or `exact=True` deliberately.

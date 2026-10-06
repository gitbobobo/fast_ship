# Issue list

The `/issues` page lists issues for the selected project with text search, state/label/source/internal-status filters, sorting, and the entry point for creating a new internal issue.

## Sub-features

- `list-select-project` scopes the list to a project via the project selector.
- `list-search` filters by title, reference, or author via the search box.
- `list-filter` narrows by state, label, source, or internal workflow status.
- `list-sort` reorders by creation/update time.
- `list-empty` shows the correct empty state for an empty project or an empty filter result.
- `list-new-entry` leads to `/projects/<pid>/issues/new` via the `新建问题` link.

## How to get to it (user POV)

- Choose the `问题` link in the sidebar.
- Click a project card on `/projects` (lands on `/issues?project=<uuid>`).
- Navigate to `/issues` directly.

## Driving it with fsdrive

Preconditions:

- Signed in (`login_ui`/`register_ui` first — the page starts on `about:blank`); a project `验证项目` exists; it contains an internal issue titled `验证问题` (create through `新建问题` if needed — see issue-detail.md).
- Get `<pid>` concretely — do not paste a literal placeholder:

  ```python
  status, body = api_get("/api/projects")
  assert status == 200
  pid = next(p["id"] for p in body["data"]["items"] if p["name"] == "验证项目")
  page.goto(BASE + f"/issues?project={pid}")
  ```

- Base UI `Select` triggers have `role="combobox"` — a nameFrom:author role, so `name=` never matches them. Always pick the trigger by displayed value: `page.get_by_role("combobox").filter(has_text="开启")`. Options inside the opened listbox do take names from content: `get_by_role("option", name="关闭")`.

- **Select project (sidebar entry).** `page.get_by_role("link", name="问题").click()` → `/issues`; when at least one project exists the selector auto-selects one and shows its name: `page.get_by_role("combobox").filter(has_text="验证项目").wait_for()`. To switch, click that combobox → `get_by_role("option", name="...")`. `shot("issues-list")`.
- **Search.** `page.get_by_placeholder("搜索标题、编号或作者").fill("验证")` keeps `验证问题` visible; `fill("zzz")` yields `没有匹配的问题` + `调整筛选条件后再试`.
- **Filter by state.** The trigger shows the current value — `开启` by default (`issueStateFilter` defaults to `open` even when no `?state=` is in the URL; the "all" label `状态` appears only after picking 全部状态):

  ```python
  page.get_by_role("combobox").filter(has_text="开启").click()
  page.get_by_role("option", name="关闭").click()            # options: 全部状态 / 开启 / 关闭
  assert "state=closed" in page.url
  page.get_by_role("combobox").filter(has_text="关闭").click()
  page.get_by_role("option", name="全部状态").click()        # "all" removes ?state= from the URL
  assert "?state=" not in page.url and "&state=" not in page.url
  ```

- **Filter by source.** `page.get_by_role("combobox").filter(has_text="来源").click()` → `option 内部` / `GitHub` / `全部来源`. Assert `source=internal` in `page.url`.
- **Filter by internal status.** `page.get_by_role("combobox").filter(has_text="内部状态").click()` → options `全部内部状态` / `未设置` / `待处理` / `开发中` / `已完成`.
- **Filter by label.** The `combobox` showing `标签` — disabled when the project has no labels.
- **Sort.** `page.get_by_role("combobox").filter(has_text="最近更新").click()` → `option 最早创建` flips ordering; assert `sort=created_asc` in `page.url`.
- **New issue entry.** With a project selected, the header shows `新建问题` — it is a **link** (`Button render={<Link/>}` renders an `<a>`): `page.get_by_role("link", name="新建问题").click()` → `page.wait_for_url("**/issues/new*")`, header `新建问题`.
- **Empty states.** No projects → `暂无项目` and no `新建问题` link. Project with zero issues still shows `没有匹配的问题` + `调整筛选条件后再试`, because the default `state=open` counts as an active filter — `暂无问题` + `可以新建内部问题，或从 GitHub 拉取问题数据` appears only with the state filter set to `全部状态`.
- **Proof.** `issues-list.png`/`issues-filtered.png` + `api_get(f"/api/projects/{pid}/issues?q=验证")` returning `验证问题` in `data.items`.

## Gotchas

- `新建问题` only renders when a project is selected — on a fresh account there is no link, only `暂无项目`.
- Selects are Base UI comboboxes, not native `<select>` — click the trigger then choose the `option` role; do not use `select_option`, and do not address triggers with `name=` (see above — combobox has no content-derived accessible name).
- Trigger names change with the current value (`开启`→`关闭`, `最近更新`→`最早创建`) — always `filter(has_text=)` on the *currently displayed* label, and re-resolve the locator after each selection.
- Search debounces (`useDeferredValue`) — wait for the row count or empty state, not a fixed sleep.
- Filter options (`全部标签`, `内部状态`) are populated per project; on a GitHub-linked project the label list differs from an internal one.
- `同步` button only makes sense for GitHub-linked projects; on a local project it opens a "需要先关联 GitHub 仓库"-style guidance dialog — do not treat it as a failure signal.
- Entry-point coverage: the sidebar `问题` link and direct `page.goto(BASE + "/issues?project=...")` are the two recipe paths above; arriving via a `/projects` card click is covered by `project-open` in projects.md.

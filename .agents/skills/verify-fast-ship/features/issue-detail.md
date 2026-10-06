# Issue detail

Creating an internal issue lands on its detail page (`/projects/<pid>/issues/<iid>`): editable title and body, workflow status, checklist, internal comments, and the read-only `人机协作区` (consensus / summary tabs written by agents via API key).

## Sub-features

- `issue-create` creates an internal issue from `/projects/<pid>/issues/new` and lands on its detail page.
- `issue-edit-title` edits the title inline via `编辑标题`.
- `issue-edit-body` opens the full edit page and saves body changes.
- `issue-comment` posts an internal comment visible in the timeline.
- `issue-checklist` adds and checks checklist items.
- `issue-collab` shows the `人机协作区` tabs (共识/完成总结), read-only in the UI.
- `issue-workflow` sets the internal workflow status.

## How to get to it (user POV)

- `/issues` → select a project → `新建问题` **link** → submit → lands on detail.
- Click any issue row in the list.
- Navigate to `/projects/<pid>/issues/<iid>` directly.

## Driving it with fsdrive

Preconditions:

- Signed in; project `验证项目` exists; resolve its id concretely:

  ```python
  status, body = api_get("/api/projects")
  assert status == 200
  pid = next(p["id"] for p in body["data"]["items"] if p["name"] == "验证项目")
  ```

- **Create.**

  ```python
  page.goto(BASE + f"/projects/{pid}/issues/new")
  page.locator("#issue-title").fill("验证问题")
  page.locator("#issue-body").fill("验证描述")          # MDEditor textarea
  page.get_by_test_id("issue-form-submit").click()     # header submit, labeled 创建问题
  # NOTE: "**/issues/*" would already match the current /issues/new — wait on a
  # predicate or the heading, never a glob that matches the source page.
  page.wait_for_url(lambda u: "/issues/" in u and not u.endswith("/new"))
  page.get_by_role("heading", name="验证问题").wait_for()  # heading renders "<reference> 验证问题"
  shot("issue-created")
  ```

  Toast `内部问题已创建` is transient — proof is the detail heading plus the db row.

- **Edit title** — inline on the detail page:

  ```python
  page.get_by_role("button", name="编辑标题").click()       # ghost button, aria-label
  page.locator("input:visible").fill("验证问题-改")          # title draft is the only visible <input>
  page.get_by_role("button", name="保存", exact=True).click()  # plain text button, NO aria-label
  page.get_by_text("验证问题-改").wait_for()
  ```

  Do **not** use `aria-label="保存"` here — that belongs to the checklist save icon and only exists while the checklist is in edit mode.

- **Edit body** — via the header `更多` menu:

  ```python
  page.get_by_role("button", name="更多").click()
  page.get_by_role("menuitem", name="编辑问题").click()
  page.wait_for_url("**/edit*")
  page.locator("#issue-body").fill("验证描述 v2")
  page.get_by_test_id("issue-form-submit").click()          # header submit, labeled 保存修改
  page.wait_for_url(lambda u: not u.endswith("/edit"))
  ```

- **Comment.** `page.get_by_placeholder("使用 Markdown 输入评论内容").fill("验证评论")` → `page.get_by_role("button", name="发布评论").click()` → the comment appears under the `内部评论` heading (toast `评论已发布`). `shot("issue-comment")`.

- **Checklist** — add then tick:

  ```python
  page.get_by_role("button", name="编辑", exact=True).click()    # checklist edit icon (title="编辑任务清单")
  page.get_by_role("button", name="添加第一项").click()          # "添加项" once items exist
  page.get_by_placeholder("任务 1").fill("验证任务")
  page.get_by_role("button", name="保存").click()                # icon button aria-label="保存", enabled only when dirty
  page.get_by_role("button", name="标记完成任务清单 1").click()    # toggle button — NOT a checkbox role
  page.reload()
  page.get_by_role("button", name="取消完成任务清单 1").wait_for() # persisted: reload shows completed state
  ```

- **Workflow** — on the detail page the selector is the `combobox` beside the `内部状态` label in the `元数据` card (there is no `#issue-workflow-status` here — that id exists only on the new/edit form):

  ```python
  page.get_by_text("内部状态", exact=True).locator("..").get_by_role("combobox").click()
  page.get_by_role("option", name="开发中").click()          # options: 未设置 / 待处理 / 开发中 / 已完成
  # toast 内部状态已更新; the workflow badge appears in the header badge row and on the list page
  ```

- **Collab area.** The `人机协作区` section renders `tab` roles `共识`/`完成总结` (aria-labels `共识，暂无内容` vs `共识，有内容`) — UI is read-only; content arrives via API-key `PUT /api/issues/<iid>/collab/*`.

- **Proof.** `issue-created.png` + `issue-comment.png`; `sqlite3 "$V/scratch/data/fast_ship.db" "select title, source from issues;" > "$V/evidence/db-issues.txt"` shows `验证问题` and `"select body from issue_comments;"` shows `验证评论`.

## Gotchas

- `data-testid="issue-form-submit"` is rendered exactly once — the header button. `InternalIssueForm` is always mounted with `hideSubmitButton` on both new and edit pages, so the in-form copy never exists; a bare `get_by_test_id` click is unambiguous.
- `#issue-body` is an MDEditor textarea — `fill` works for text; paste/image flows differ, keep to text.
- Comments on internal issues stay internal (`内部评论`); they never reach GitHub.
- GitHub-sourced issues are read-only — `编辑标题` and the edit route render a `GitHub 问题为只读` card; verify against an `internal` issue.
- Checklist edits are draft until `保存` is clicked — reloading an unsaved draft loses it. Toggling an item's done state in read mode persists immediately (no edit mode needed).
- On the detail page, `内部状态` is a plain `<span>` label, not a form `Label` — `get_by_label` does not reach the combobox; scope via the label's parent as shown.
- While the title editor is open its `保存`/`取消` text buttons coexist with the checklist icon buttons — `exact=True` on `保存` is what disambiguates.

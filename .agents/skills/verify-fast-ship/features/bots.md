# Bots

The `/bots` surface is a three-column workspace (INT-125): a left column listing the user's bots, the middle column is that bot's single main conversation line, and a hideable right column shows the bot's details — identity, edit/authorize entries, schedule tasks, and a config summary. Selecting a bot in the left column is what decides which conversation the middle column shows.

## Sub-features

- `bot-list` renders every bot in the left column with a deterministic color avatar, its name, and a one-line preview of the latest mainline message (user messages carry a `你：` prefix). The preview value comes from the list endpoint; committing a mainline message makes the client re-read the list, so the row follows the conversation without a page reload.
- `bot-create` opens the `新建机器人` dialog and persists a bot from name, persona, endpoint, model and key.
- `bot-open` selects a bot in the left column, which navigates to `/bots/<uuid>` and fills the middle (chat) and right (details) columns.
- `bot-detail` shows the selected bot's avatar, name, model, `编辑` / `授权管理` entries, a config summary (model, API address, key last four, context window, created time) and a `删除机器人` entry.
- `bot-detail-hide` collapses the right column with `隐藏详情` and brings it back with `显示详情`.
- `bot-schedule` lists the bot's schedule tasks with their plan, next run, last result and an enable/disable switch; clicking a row opens a read-only instruction + run-history dialog.
- `bot-chat` is the middle column: send a message from the composer at the bottom of the column.
- `bot-mobile-detail` opens the same detail panel in a right-side sheet from the `详情` header button below the `lg` breakpoint.

## How to get to it (user POV)

- Choose `机器人` in the sidebar.
- Navigate to `/bots` (empty middle column) or `/bots/<uuid>` (a selected bot) directly.
- Click any row in the left column.

## Driving it with fsdrive

Preconditions:

- Signed in. In a `run` scenario the page starts on `about:blank` — call `register_ui(...)`/`login_ui(...)` first, or `page.goto(BASE + "/bots")`.
- At least one bot exists (see the create bullet; a dummy API key is fine — nothing is sent upstream until you actually chat).

All commands assume a signed-in scenario on a rendered page, driven at the default 1440x900 viewport (so `lg` layout is active).

- **Empty list.** On a fresh account, `page.goto(BASE + "/bots")`: header `机器人`, left column `还没有机器人` + `点击右上角「+」新建`, and the middle column shows `从左侧选择一个机器人`. `shot("bots-empty")`.
- **Create.** `page.get_by_role("button", name="新建机器人").first.click()` opens dialog `新建机器人`; fill `get_by_label("名称" | "人设提示词" | "API 地址" | "模型" | "API Key")` and submit with `page.get_by_role("dialog").get_by_role("button", name="保存").click()`. The dialog closes, toast `机器人已创建` appears, and the new bot shows up in the left column. `shot("bots-list")`.
- **List and preview.** Each row is a link whose accessible name is exactly the bot name (`aria-label`): `page.get_by_role("link", name="Code Bot")`; the visible preview line comes from `GET /api/bots` → `last_message.text` (mainline only, empty/tool-call-only messages are skipped server-side).
- **Open.** `page.get_by_role("link", name="Code Bot").first.click()` → URL `/bots/<uuid>`, header title becomes the bot name, the left row carries `aria-current="page"`, the middle column shows the conversation and composer, and the right column shows the details. `shot("bot-detail")`.
- **Hide / show the panel.** `page.get_by_role("button", name="隐藏详情").first.click()` removes the right column; `page.get_by_role("button", name="显示详情").click()` brings it back. Below `lg` the detail lives in a right sheet opened by `page.get_by_role("button", name="详情")`, whose dialog name is `<bot>详情`; the `机器人列表` link (`page.get_by_role("link", name="机器人列表")`, an `<a href="/bots">`) is the always-available way back to the list (the header's back arrow is disabled on a cold deep link).
- **Preview follows the chat.** Send a message and the matching left-column row shows `你：<text>` without a page reload; an assistant reply replaces it once the message is committed. The row is server-computed: a commit re-reads the list (`GET /api/bots` is requested per committed mainline message) rather than patching the cache client-side.
- **Edit / authorizations.** In the right column: `page.get_by_role("button", name="编辑")` opens the edit dialog (`名称` prefilled, key field empty with `末四位 <last4>` hint); `page.get_by_role("button", name="授权管理")` opens the authorization list with per-row `撤销` buttons.
- **Schedule tasks.** The `定时任务` section is expanded by default inside the right column: `page.get_by_role("button", name=/定时任务 · \d+ 个/)` toggles it (`aria-expanded`), rows carry `role="switch"` named `停用 <name>` / `启用 <name>`, and clicking a task row opens its instruction + run-history dialog. Tasks are created only by the bot through its tools — this page is deliberately read-only plus enable/disable.
- **Delete.** `page.get_by_role("button", name="删除机器人").click()` → alertdialog `删除机器人？` → `确认删除`. The bot disappears and the app navigates back to `/bots`.
- **Proof.** `shot()` pairs a `.png` with an `.aria.txt`; read the same data back with `api_get("/api/bots")` (list items carry `last_message`) or `sqlite3 "$V/scratch/data/fast_ship.db" "select name from bots;"`.

## Gotchas

- On an empty account there are **two** buttons named `新建机器人` (left column header `+` and the middle-column CTA) — scope with `.first` or by column.
- While the right column is open, `隐藏详情` also names the panel's own `X` button; use `.first` or scope to `get_by_test_id("bot-detail-panel")`.- The 定时任务 section is a collapsible button, not a plain heading: check `aria-expanded` before clicking, or you will collapse the section instead of expanding it.
- `last_message` is list-only: `GET /api/bots/<uuid>` returns `last_message: null`, yet the same bot's row in the left column shows a preview — that is expected, not a bug.
- The pre-INT-125 UI was a table on `/bots` with a separate detail page; recipes or screenshots of that table are stale.
- Switching bots keeps the right column mounted with a skeleton; `bot-detail-skeleton` only means the detail query is still in flight, not that the bot is missing.

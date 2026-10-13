# GitHub Tokens

The `/settings/github-tokens` page manages the account's GitHub Token library. Tokens are added once and referenced by projects for two independent purposes: the public-repo token (issue sync + GitHub writes) and the code-repo token (reading linked PRs). The library never shows or echoes token plaintext.

## Sub-features

- `token-create` adds a token with a title; the server detects it once right after saving.
- `token-list` shows title, `ghp_…`-style prefix, detection status (未检测/认证成功/凭证无效/检测失败), token type with scopes or the fine-grained notice, referencing project names, and last detection time.
- `token-edit` renames a token; replacing the secret lists affected projects for confirmation before it applies to all references.
- `token-redetect` refreshes the read-only detection on demand.
- `token-delete` is refused while any project references the token and confirmed when unreferenced.
- `project-reference` picks library tokens in the project form via two selects (公开仓库 Token / 代码仓库 Token), both optional, with an inline 新建 Token entry that auto-selects the new record.

## How to get to it (user POV)

- Sidebar `设置` → nav item `GitHub Tokens`, or navigate to `/settings/github-tokens` directly.
- Project reference selectors live in the project create/edit dialog (`新建项目` / card `更多操作` → `编辑`).
- Sync entry point that depends on the public-repo token is the `同步` button on `/issues`.

## Driving it with fsdrive

Preconditions:

- Signed in (`login_ui`/`register_ui` first). A token value for driving: create a throwaway classic PAT out-of-band — never paste real tokens into notes or evidence; use `save_secret()`.
- Fresh account has an empty library (`暂无 GitHub Token`).

- **Open the page.** `page.goto(BASE + "/settings/github-tokens")`; header `GitHub Tokens`, intro copy mentions 公开仓库 Token and 代码仓库 Token, empty state `暂无 GitHub Token`. `shot("tokens-empty")`.
- **Create.** `page.get_by_role("button", name="添加 Token").click()`; fill `page.get_by_label("标题")` with `verify-token` and `page.get_by_label("Token")` with the secret; submit `page.get_by_role("dialog").get_by_role("button", name="保存")`. Dialog closes, toast `GitHub Token 已保存`, and the row shows `verify-token`, prefix, and a status badge (`认证成功` with a valid token, `凭证无效` for a bogus one — both still save). `shot("token-created")`. Proof via `api_get("/api/github-tokens")` — response contains `title` and `token_prefix` but never the secret.
- **Invalid token.** Repeat create with `ghp_invalid` — row saves with badge `凭证无效` (HTTP stays 200; detection status is data, not an error).
- **Rename.** Row action button `编辑 verify-token` → change `标题` → `保存` → toast `标题已更新`.
- **Replace with confirmation.** In the edit dialog fill `替换密钥（可选）`, submit once → a confirmation block lists affected projects (`该 Token 被 N 个项目引用`), submit again (`确认替换`) → toast `Token 已替换并对所有引用生效`.
- **Re-detect.** Row action `刷新检测 verify-token` → toast reflects the result; `最近检测` column updates.
- **Delete guard.** With a project referencing the token (see below), row action `删除 verify-token` → dialog explains references; `确认删除` → toast `删除失败：该 Token 仍被项目引用…` and the row stays. After clearing the reference the same flow deletes the row.
- **Reference from a project.** `page.goto(BASE + "/projects")` → `创建项目` → the dialog shows `公开仓库 Token（可选）` and `代码仓库 Token（可选）` selects listing `verify-token（<prefix>••••）`; pick one, submit, reopen `编辑` — the select still shows the chosen token. The `+` button beside a select opens the same create dialog and auto-selects the new record. Repo URL alone still creates fine with both selects at `不使用`.
- **Sync gating.** On `/issues` with a project that has a repo but no public token: `同步` click → toast `该项目未配置公开仓库 Token…` without a network sync; previously synced issues still render.

## Gotchas

- Token plaintext is never rendered anywhere — assertions must target `token_prefix`/`title`/status badges, and evidence must not contain the secret.
- Detection outcomes are data on the row (`检测失败` ≠ `凭证无效`); a network failure during verification does not invalidate the token.
- The edit dialog needs two submits when replacing a secret (fill → confirm block appears → `确认替换`); a single submit only reveals the confirmation.
- Two selects share option titles with the settings page — scope `get_by_role("option")` to the open listbox.
- `添加 Token` appears in both the settings header and the project dialog's `+` icons; scope to the dialog when driving from the project form.

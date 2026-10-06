# API keys

Settings → API Keys lets a user create `fsk_` tokens for automation (CI/CD, agents), see them listed with prefix and usage, and delete them. The full key is shown exactly once in a post-create dialog.

## Sub-features

- `key-create` creates a named key via the `创建 API Key` dialog.
- `key-reveal` shows the full `fsk_` key once, with a copy button.
- `key-list` renders keys in a table (name, prefix, created, last used).
- `key-delete` removes a key through a confirmation dialog.

## How to get to it (user POV)

- User menu → `设置` → `API Keys` nav link.
- Navigate to `/settings/api-keys` directly.

## Driving it with fsdrive

Preconditions:

- Signed in; no key named `verify-ci` exists.
- **The full key is a credential — it must never land in `evidence/`** (no `.png`, `.aria.txt`, run notes, or report). It goes to scratch via `save_secret()`; screenshots of the page happen only after the reveal dialog is closed.

- **Open page.** `page.goto(BASE + "/settings/api-keys")` — header `设置`, section `API Key 管理`, empty state `暂无 API Key` on a fresh account. `shot("apikeys-empty")`.

- **Create + capture key (scratch only).** The reveal dialog is a Base UI `div[role="dialog"]`, not a `<dialog>` element — CSS `dialog` will not match it:

  ```python
  page.get_by_role("button", name="创建 API Key").click()
  dialog = page.get_by_role("dialog")
  dialog.get_by_label("备注名称").fill("verify-ci")
  dialog.get_by_role("button", name="创建", exact=True).click()

  # dialog switches to "API Key 创建成功"; the <code> inside holds the full key
  reveal = page.get_by_role("dialog")
  key = reveal.locator("code").inner_text()
  assert key.startswith("fsk_"), key
  save_secret("api-key.txt", key)          # → $V/scratch/api-key.txt, mode 0600
  reveal.get_by_role("button", name="我已保存").click()

  # shot AFTER closing — the table proves the key exists without exposing it
  page.get_by_text("verify-ci").wait_for()
  shot("apikeys-list")                     # prefix column shows fsk_****•••• only
  ```

- **Use it (side proof)** — back in bash, read the key from scratch; `code` must be `200`:

  ```bash
  KEY="$(cat "$V/scratch/api-key.txt")"
  code="$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 3 --max-time 5 \
    -H "Authorization: Bearer $KEY" "http://127.0.0.1:$PORT/api/projects")"
  [ "$code" = "200" ] && echo "key works" || echo "FAIL: got $code"
  ```

- **Delete.** `page.get_by_label("删除 API Key verify-ci").click()` → AlertDialog `删除 API Key?` → `page.get_by_role("button", name="确认删除").click()`. Row disappears; toast `API Key 已删除`. Re-run the curl — now the code must be `401`.

- **Proof.** `apikeys-list.png` (verify-ci row, masked prefix) + two curl codes (200 then 401) recorded in `run.txt` (codes only — never the key) + `sqlite3 "$V/scratch/data/fast_ship.db" "select name, key_prefix from api_keys;"`.

## Gotchas

- The full key exists only in the `API Key 创建成功` dialog — after `我已保存` it is unrecoverable. Grab it via `save_secret` before closing.
- Do not screenshot the reveal dialog: the `<code>` text lands verbatim in both the `.png` and the `.aria.txt`. The proof shot is the list after closing — `key_prefix` + `••••••••` is all the evidence a reviewer needs.
- Deleting a key is immediate — a second `curl` with the same key returning 401 is the clean side-effect proof.
- `复制 API Key` needs clipboard permissions; assert the `<code>` text instead of the clipboard.
- The settings nav link is `API Keys` (plural) while the dialog button is `创建 API Key`.

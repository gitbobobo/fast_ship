# Fast Ship verification map

This directory is the maintained source for verifying the user-facing behavior of Fast Ship's web UI. Read the index before driving the app, then use the matching feature file as the recipe.

## Baseline preconditions

- Launch Fast Ship on `http://127.0.0.1:$PORT` with a disposable sqlite database and uploads dir under the run's `mktemp -d /tmp/fast-ship-verify/run-*/scratch/` (see SKILL.md → Launch).
- Never drive an instance that was not started by this verification run — port 4888/4999 and any PID not in `scratch/server.pid` belong to someone else.
- Run the Doctor block from SKILL.md — it exits non-zero on any failed check (dead process, foreign port owner, probe not answering 401, stale bundle hash); do not drive until it prints `DOCTOR OK`.
- Register a fresh account through `/register` (open registration, auto-logs-in). Credentials and tokens (including API keys) stay in `scratch/` via `save_secret()` — never in `evidence/`, never in run notes or the report.
- Put `fsdrive.py` on the command line as `$DRV` with `--base`, `--evidence`, and `--state` set.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Prefer ARIA roles and accessible names, then label/ID/placeholder selectors, then route paths — never coordinates or tab order.
- Treat every command as literal. Chinese labels (`项目`, `创建项目`, `发布评论`, ...) are matched exactly as written.
- Browser actions run through `fsdrive.py` commands or `run` scenario helpers (`page`, `shot`, `register_ui`, `login_ui`, `api_get`).
- Read-back of side effects may use `api_get` (JWT from the browser session) or `sqlite3` on the scratch db; state changes always go through the UI.
- Names and titles created during a run should carry a distinctive prefix (e.g. `verify-`) so they are attributable.

## Proof and skip reporting

- Capture the user action and the resulting state, not only the final screen — `shot()` pairs a `.png` with an `.aria.txt`.
- Mutation proof includes a read-only second view: another route, `api_get`, or a sqlite row — not only the toast.
- Record the feature ID and entry point used with every artifact.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report a skipped entry point as verified through a different path.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the user-visible behavior. It then uses exactly four H2 sections in this order.

1. `Sub-features` lists short IDs with one line for each behavior.
2. `How to get to it (user POV)` lists every user entry point.
3. `Driving it with fsdrive` starts with `Preconditions:` and uses labeled bullets that pair each user action with an exact command and observable result.
4. `Gotchas` lists traps that can waste or invalidate a verification run.

Keep implementation details out of the map. Name only user paths, stable handles, required state, commands, and observable proof.

## Features

- [Sign up, sign in, sign out](./auth.md) covers open registration, login, route guarding, session persistence, and logout.
- [Projects](./projects.md) covers creating, searching, opening, editing, and deleting a project.
- [Issue list](./issues-list.md) covers the project selector, search, filters, sorting, and the new-issue entry point.
- [Issue detail](./issue-detail.md) covers creating an internal issue, editing, comments, checklist, and the collaboration area.
- [API keys](./api-keys.md) covers creating, copying, listing, and deleting API keys.

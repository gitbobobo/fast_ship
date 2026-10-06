#!/usr/bin/env python3
"""Headless driver for Fast Ship web UI verification.

Usage:
  python3 fsdrive.py --base http://127.0.0.1:5088 --evidence /tmp/fast-ship-verify/<run>/evidence <command> [args]

Commands:
  register <username> <email> <password>   Register through the /register form, land on /dashboard.
  login <login> <password>                 Log in through the /login form (username or email), land on /dashboard.
  shot <path> <name>                       Navigate to <path> and capture <name>.png + <name>.aria.txt.
  run <scenario.py>                        Execute a scenario file with helpers injected (see below).

Scenario files are plain Python executed with these names pre-bound:
  page                 playwright Page (fresh context, or --state if given)
  shot(name)           save evidence/<name>.png and evidence/<name>.aria.txt
  register_ui(u, e, p) drive the /register form end to end
  login_ui(l, p)       drive the /login form end to end
  jwt()                read the access token back out of localStorage
  api_get(path)        GET <base><path> with the current JWT; returns (status, parsed_json)
  save_secret(n, v)    write a credential to the scratch dir (0600), return its path.
                       Secrets MUST go here — never into evidence files or screenshots.
  persist_state()      write the current context's storage state to --state
  BASE, EVIDENCE, SCRATCH  the --base URL, --evidence dir, and scratch dir

Options:
  --base URL      required. Origin of the running Fast Ship server.
  --evidence DIR  required. Directory for screenshots and snapshots (created if missing).
  --state FILE    optional. Playwright storage-state JSON; loaded if it exists and
                  written back after register/login. Lets separate invocations share a session.
  --scratch DIR   optional. Directory for secrets and run-local state
                  (default: sibling "scratch" of --evidence).
  --timeout MS    per-action Playwright timeout (default 15000). api_get gets the
                  same budget in seconds.

Requires: python3 -m playwright install chromium (once, user-level cache).
If the bundled chromium executable is absent, falls back to the system
Google Chrome — still headless. Any other launch error aborts immediately.
Everything runs headless; no windows are opened.
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

from playwright.sync_api import sync_playwright


def main() -> int:
    parser = argparse.ArgumentParser(prog="fsdrive.py")
    parser.add_argument("--base", required=True, help="e.g. http://127.0.0.1:5088")
    parser.add_argument("--evidence", required=True, help="evidence directory")
    parser.add_argument("--state", default=None, help="storage-state JSON file")
    parser.add_argument("--scratch", default=None, help="scratch directory for secrets")
    parser.add_argument("--timeout", type=int, default=15000)
    parser.add_argument("command", choices=["register", "login", "shot", "run"])
    parser.add_argument("args", nargs="*")
    ns = parser.parse_args()

    evidence = Path(ns.evidence)
    evidence.mkdir(parents=True, exist_ok=True)
    scratch = Path(ns.scratch) if ns.scratch else evidence.parent / "scratch"
    state_path = Path(ns.state) if ns.state else None
    if state_path and not state_path.exists():
        state_path = None

    with sync_playwright() as pw:
        try:
            browser = pw.chromium.launch(headless=True)
        except Exception as exc:
            # Only an absent bundled executable may fall back to system Chrome;
            # every other launch failure is a real problem and aborts the run.
            if "Executable doesn't exist" not in str(exc) and "playwright install" not in str(exc):
                raise
            print(
                f"fsdrive: bundled chromium missing ({exc}); "
                "falling back to system Chrome",
                file=sys.stderr,
            )
            browser = pw.chromium.launch(headless=True, channel="chrome")
        context_kwargs = {"viewport": {"width": 1440, "height": 900}}
        if state_path:
            context_kwargs["storage_state"] = str(state_path)
        context = browser.new_context(**context_kwargs)
        context.set_default_timeout(ns.timeout)
        page = context.new_page()

        def shot(name: str) -> None:
            try:
                page.wait_for_load_state("networkidle", timeout=3000)
            except Exception:
                pass  # best effort: capture even if requests are still settling
            page.screenshot(path=str(evidence / f"{name}.png"), full_page=True)
            aria = page.locator("body").aria_snapshot()
            (evidence / f"{name}.aria.txt").write_text(aria, encoding="utf-8")

        def settle_dashboard() -> None:
            # Lazy chunks render "页面加载中..." while they load; wait it out best-effort.
            try:
                page.get_by_text("页面加载中...").wait_for(state="detached", timeout=5000)
            except Exception:
                pass

        def fresh_auth_page(path: str) -> None:
            # A live session redirects /login and /register to /dashboard.
            # Land once, clear the persisted token, then land again so the form renders.
            page.goto(ns.base + path, wait_until="networkidle")
            page.evaluate("localStorage.removeItem('fast-ship-auth')")
            page.goto(ns.base + path, wait_until="networkidle")

        def fill_register(username: str, email: str, password: str) -> None:
            page.get_by_label("用户名").fill(username)
            page.get_by_label("邮箱").fill(email)
            page.get_by_label("密码").fill(password)

        def submit_register() -> None:
            page.get_by_role("button", name="注册").click()
            page.wait_for_url("**/dashboard")
            page.get_by_role("link", name="仪表盘").wait_for()
            settle_dashboard()

        def fill_login(login: str, password: str) -> None:
            page.get_by_label("用户名或邮箱").fill(login)
            page.get_by_label("密码").fill(password)

        def submit_login() -> None:
            page.get_by_role("button", name="登录").click()
            page.wait_for_url("**/dashboard")
            page.get_by_role("link", name="仪表盘").wait_for()
            settle_dashboard()

        def register_ui(username: str, email: str, password: str) -> None:
            fresh_auth_page("/register")
            fill_register(username, email, password)
            submit_register()

        def login_ui(login: str, password: str) -> None:
            fresh_auth_page("/login")
            fill_login(login, password)
            submit_login()

        def jwt() -> str:
            raw = page.evaluate("localStorage.getItem('fast-ship-auth')")
            if not raw:
                raise RuntimeError("no fast-ship-auth in localStorage")
            return json.loads(raw)["state"]["token"]

        def api_get(path: str):
            req = urllib.request.Request(
                ns.base + path,
                headers={"Authorization": f"Bearer {jwt()}"},
            )
            try:
                with urllib.request.urlopen(
                    req, timeout=max(ns.timeout / 1000.0, 1.0)
                ) as res:
                    return res.status, json.loads(res.read())
            except urllib.error.HTTPError as exc:
                body = exc.read()
                try:
                    return exc.code, json.loads(body)
                except json.JSONDecodeError:
                    return exc.code, body.decode("utf-8", "replace")

        def save_secret(name: str, value: str) -> str:
            # Credentials are private to the run: scratch only, mode 0600,
            # never under --evidence.
            if os.sep in name or (os.altsep and os.altsep in name) or name.startswith("."):
                raise ValueError(f"secret name must be a plain filename: {name!r}")
            scratch.mkdir(parents=True, exist_ok=True)
            target = scratch / name
            target.write_text(value, encoding="utf-8")
            target.chmod(0o600)
            return str(target)

        def persist_state() -> None:
            if ns.state:
                Path(ns.state).parent.mkdir(parents=True, exist_ok=True)
                context.storage_state(path=ns.state)

        try:
            if ns.command == "register":
                username, email, password = ns.args
                fresh_auth_page("/register")
                fill_register(username, email, password)
                shot("register-form")  # filled form, before submit
                submit_register()
                shot("register-dashboard")
                persist_state()
            elif ns.command == "login":
                login, password = ns.args
                fresh_auth_page("/login")
                fill_login(login, password)
                shot("login-form")  # filled form, before submit
                submit_login()
                shot("login-dashboard")
                persist_state()
            elif ns.command == "shot":
                path, name = ns.args
                page.goto(ns.base + path, wait_until="networkidle")
                shot(name)
            elif ns.command == "run":
                (scenario_path,) = ns.args
                scenario = Path(scenario_path).read_text(encoding="utf-8")
                globs = {
                    "page": page,
                    "shot": shot,
                    "register_ui": register_ui,
                    "login_ui": login_ui,
                    "jwt": jwt,
                    "api_get": api_get,
                    "save_secret": save_secret,
                    "persist_state": persist_state,
                    "BASE": ns.base,
                    "EVIDENCE": str(evidence),
                    "SCRATCH": str(scratch),
                }
                exec(compile(scenario, str(scenario_path), "exec"), globs)
                persist_state()
        finally:
            browser.close()

    return 0


if __name__ == "__main__":
    sys.exit(main())

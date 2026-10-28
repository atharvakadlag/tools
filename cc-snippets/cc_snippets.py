#!/usr/bin/env python3
"""iTerm2 AutoLaunch script: Toolbelt panel + copy_snippet(n) key-bindable function for Claude Code snippets."""
import asyncio
import json
import os
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import iterm2

DATA_DIR = os.path.expanduser("~/.claude/cc-snippets")
PORT = 47911
PAGE = os.path.join(os.path.dirname(os.path.realpath(__file__)), "panel.html")
focused = {"id": None}
last_copied = {"session": None, "n": None, "at": 0}


def load(session_id):
    try:
        with open(os.path.join(DATA_DIR, f"{session_id}.json")) as f:
            return json.load(f)
    except (OSError, ValueError, TypeError):
        return {"snippets": []}


def copy(session_id, n):
    """n is 1-based; 0 means the last snippet."""
    snippets = load(session_id)["snippets"]
    if not snippets:
        return None
    n = len(snippets) if n == 0 else n
    if not 1 <= n <= len(snippets):
        return None
    subprocess.run(["pbcopy"], input=snippets[n - 1]["code"].encode(), env={**os.environ, "LANG": "en_US.UTF-8"})
    last_copied.update(session=session_id, n=n, at=time.time())
    return n


class Handler(BaseHTTPRequestHandler):
    def _send(self, body, ctype):
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/data":
            sid = focused["id"]
            copied = last_copied if last_copied["session"] == sid else {"n": None, "at": 0}
            self._send(json.dumps({**load(sid), "copied": copied}).encode(), "application/json")
        else:
            with open(PAGE, "rb") as f:
                self._send(f.read(), "text/html; charset=utf-8")

    def do_POST(self):
        n = int(parse_qs(urlparse(self.path).query).get("n", ["0"])[0])
        self._send(json.dumps({"copied": copy(focused["id"], n)}).encode(), "application/json")

    def log_message(self, *args):
        pass


async def track_focus(connection, app):
    window = app.current_terminal_window
    if window and window.current_tab:
        focused["id"] = window.current_tab.current_session.session_id
    async with iterm2.FocusMonitor(connection) as monitor:
        while True:
            update = await monitor.async_get_next_update()
            session = None
            if update.active_session_changed:
                session = app.get_session_by_id(update.active_session_changed.session_id)
            elif update.selected_tab_changed:
                tab = app.get_tab_by_id(update.selected_tab_changed.tab_id)
                session = tab and tab.current_session
            elif update.window_changed:
                window = app.get_window_by_id(update.window_changed.window_id)
                session = window and window.current_tab and window.current_tab.current_session
            if session:
                focused["id"] = session.session_id


async def main(connection):
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()

    app = await iterm2.async_get_app(connection)
    await iterm2.tool.async_register_web_view_tool(
        connection, "Claude Snippets", "com.atharva.cc-snippets", False, f"http://127.0.0.1:{PORT}/"
    )

    @iterm2.RPC
    async def copy_snippet(n=0, session_id=iterm2.Reference("id")):
        copied = copy(session_id or focused["id"], int(n))
        msg = f"Copied snippet #{copied}" if copied else f"No snippet #{n} in this tab"
        subprocess.Popen(["osascript", "-e", f'display notification "{msg}" with title "Claude Snippets"'])

    await copy_snippet.async_register(connection)
    await track_focus(connection, app)


iterm2.run_forever(main)

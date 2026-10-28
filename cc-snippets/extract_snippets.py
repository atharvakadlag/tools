#!/usr/bin/env python3
"""Claude Code Stop hook: saves the code blocks from the last reply to ~/.claude/cc-snippets/<iterm session>.json."""
import json
import os
import re
import sys
import time

OUT_DIR = os.path.expanduser("~/.claude/cc-snippets")
FENCE = re.compile(r"^(\s*)(`{3,}|~{3,})\s*([^\s`]*)")


def last_reply_text(transcript_path):
    texts = []
    with open(transcript_path) as f:
        for line in f:
            try:
                entry = json.loads(line)
            except ValueError:
                continue
            if entry.get("isSidechain") or entry.get("type") not in ("user", "assistant"):
                continue
            content = entry.get("message", {}).get("content")
            if entry["type"] == "user":
                # A tool_result is part of the same turn; anything else is a new prompt.
                if isinstance(content, str) or not any(c.get("type") == "tool_result" for c in content or []):
                    texts = []
                continue
            for c in content or []:
                if c.get("type") == "text":
                    texts.append(c["text"])
    return "\n".join(texts)


def code_blocks(text):
    blocks, cur = [], None
    for line in text.splitlines():
        if cur is None:
            m = FENCE.match(line)
            if m:
                cur = {"indent": len(m.group(1)), "fence": m.group(2), "lang": m.group(3), "lines": []}
        elif line.strip().startswith(cur["fence"]) and not line.strip().lstrip(cur["fence"][0]):
            blocks.append({"lang": cur["lang"], "code": "\n".join(cur["lines"])})
            cur = None
        else:
            # Blocks nested in list items are indented; strip that indent from each line.
            cur["lines"].append(line[cur["indent"]:] if line[: cur["indent"]].isspace() else line.lstrip())
    return [b for b in blocks if b["code"].strip()]


def main():
    hook = json.load(sys.stdin)
    text = hook.get("last_assistant_message") or last_reply_text(hook["transcript_path"])
    blocks = code_blocks(text)
    # Keep the previous snippets when the latest reply has none.
    if not blocks:
        return
    data = {"updated": time.time(), "project": os.path.basename(hook.get("cwd", "")), "snippets": blocks}
    # ITERM_SESSION_ID is "w0t1p0:<uuid>"; the uuid matches iterm2.Session.session_id.
    session = os.environ.get("ITERM_SESSION_ID", "").rpartition(":")[2] or "default"
    os.makedirs(OUT_DIR, exist_ok=True)
    out = os.path.join(OUT_DIR, session + ".json")
    with open(out + ".tmp", "w") as f:
        json.dump(data, f)
    os.replace(out + ".tmp", out)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"cc-snippets: {e}", file=sys.stderr)

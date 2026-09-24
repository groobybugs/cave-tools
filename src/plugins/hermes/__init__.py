"""Hermes plugin adapter for cave-tools tool redirection.

Mirrors the Claude/Grok/Gemini ``cave-tools-redirect.sh`` hook and the OpenCode
``tool.execute.before`` plugin: at ``enforce`` it blocks the built-in read and
search tools so the model reaches for ``cave__*`` instead; at ``strict`` it also
gates edits behind a prior ``cave__read`` of the same path.

``terminal`` is left alone on purpose — the rtk-rewrite plugin already rewrites
shell commands, so blocking it here would only cost a turn.
"""

import os
import stat
import sys

VALID_MODES = ("off", "hint", "enforce", "strict")
MAX_FLAG_BYTES = 32
DEFAULT_MODE = "enforce"

READ_TOOLS = {"read_file"}
SEARCH_TOOLS = {"search_files"}
EDIT_TOOLS = {"write_file", "patch"}

IMAGE_SUFFIXES = (".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".pdf", ".svg")

DENIALS = {
    "read": "Use cave__read instead of read_file — optimized drop-in (dedup + line budgets).",
    "search": "Use cave__grep instead of search_files — optimized drop-in (ripgrep + line budgets).",
    "edit": "STRICT: cave__read this file first before editing — keeps the dedup cache coherent.",
}


def register(ctx):
    """Register the Hermes pre-tool callback."""
    ctx.register_hook("pre_tool_call", _pre_tool_call)


def _claude_dir():
    return os.environ.get("CLAUDE_CONFIG_DIR") or os.path.join(os.path.expanduser("~"), ".claude")


def _read_mode():
    """Read the shared mode flag, refusing symlinks and oversized files."""
    path = os.path.join(_claude_dir(), ".cave-tools-active")
    try:
        st = os.lstat(path)
        if not stat.S_ISREG(st.st_mode) or st.st_size > MAX_FLAG_BYTES:
            return DEFAULT_MODE
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open(path, flags)
        try:
            raw = os.read(fd, MAX_FLAG_BYTES).decode("utf-8", "replace")
        finally:
            os.close(fd)
    except Exception:
        return DEFAULT_MODE
    raw = raw.strip().lower()
    return raw if raw in VALID_MODES else DEFAULT_MODE


def _file_path(args):
    if not isinstance(args, dict):
        return ""
    for key in ("file_path", "path", "filePath", "target_file", "file"):
        value = args.get(key)
        if isinstance(value, str) and value:
            return value
    return ""


def _was_read_via_cave(path):
    if not path:
        return False
    registry = os.path.join(_claude_dir(), "cave-tools", "read-registry.txt")
    try:
        st = os.lstat(registry)
        if not stat.S_ISREG(st.st_mode):
            return False
        with open(registry, "r", encoding="utf-8", errors="replace") as fh:
            return any(line.strip() == path for line in fh)
    except Exception:
        return False


def _pre_tool_call(tool_name=None, args=None, **_kwargs):
    """Block built-ins that cave-tools replaces, per the active mode."""
    try:
        mode = _read_mode()
        if mode in ("off", "hint"):
            return

        if not isinstance(tool_name, str) or "cave__" in tool_name:
            return

        if tool_name in READ_TOOLS:
            path = _file_path(args)
            if path.lower().endswith(IMAGE_SUFFIXES):
                return  # built-in still handles images directly
            return {"action": "block", "message": DENIALS["read"]}

        if tool_name in SEARCH_TOOLS:
            return {"action": "block", "message": DENIALS["search"]}

        if mode == "strict" and tool_name in EDIT_TOOLS:
            path = _file_path(args)
            if path and not _was_read_via_cave(path):
                return {"action": "block", "message": DENIALS["edit"]}
    except Exception as e:
        _warn(str(e))
    return


def _warn(message):
    print(f"cave-tools: hermes plugin warning: {message}", file=sys.stderr)

"""Connection layer between the MCP server and a running Unreal Editor.

Talks to the editor over the Python plugin's remote execution channel
(UDP multicast discovery + TCP command connection). The editor must be
running with remote execution enabled (see Config/DefaultEngine.ini).
"""
import json
import threading
import time

import remote_execution as remote

JSON_MARKER = "__MCP_JSON__"

_LOCK = threading.Lock()
_CONNECTION = None


class UnrealNotRunning(RuntimeError):
    pass


def _connect():
    conn = remote.RemoteExecution()
    conn.start()
    deadline = time.time() + 5.0
    while time.time() < deadline and not conn.remote_nodes:
        time.sleep(0.1)
    nodes = conn.remote_nodes
    if not nodes:
        conn.stop()
        raise UnrealNotRunning(
            "No Unreal Editor instance found. Make sure the editor is running "
            "with the project open (remote execution is enabled in DefaultEngine.ini)."
        )
    conn.open_command_connection(nodes[0]["node_id"])
    return conn


def _get_connection():
    global _CONNECTION
    if _CONNECTION is None:
        _CONNECTION = _connect()
    return _CONNECTION


def _drop_connection():
    global _CONNECTION
    if _CONNECTION is not None:
        try:
            _CONNECTION.stop()
        except Exception:
            pass
        _CONNECTION = None


def run_python(code, exec_mode=remote.MODE_EXEC_FILE):
    """Run python code in the editor. Returns the raw result dict:
    {'success': bool, 'result': str, 'output': [{'type', 'output'}, ...]}
    """
    # ExecuteFile mode mis-parses code that STARTS with a quoted string (e.g.
    # a module docstring) as a quoted file path. A leading comment avoids that.
    if exec_mode == remote.MODE_EXEC_FILE:
        code = "# mcp\n" + code

    with _LOCK:
        last_error = None
        for attempt in range(2):
            try:
                conn = _get_connection()
                return conn.run_command(code, exec_mode=exec_mode, raise_on_failure=False)
            except UnrealNotRunning:
                _drop_connection()
                raise
            except Exception as exc:  # stale socket after editor restart -> reconnect once
                last_error = exc
                _drop_connection()
        raise UnrealNotRunning(f"Lost connection to Unreal Editor: {last_error}")


def format_output(result):
    """Flatten a result dict into readable text."""
    lines = []
    for entry in result.get("output") or []:
        text = entry.get("output", "").rstrip("\n")
        kind = entry.get("type", "Info")
        if kind in ("Error", "Warning"):
            lines.append(f"[{kind}] {text}")
        else:
            lines.append(text)
    if result.get("result") not in (None, "", "None"):
        lines.append(str(result["result"]))
    if not result.get("success"):
        lines.insert(0, "[Execution failed]")
    return "\n".join(lines) if lines else "(no output)"


def run_json(snippet):
    """Run a snippet that prints JSON_MARKER + json payload; return the parsed payload.

    Raises RuntimeError with the editor's log output if the snippet failed.
    """
    result = run_python(snippet)
    for entry in result.get("output") or []:
        text = entry.get("output", "")
        idx = text.find(JSON_MARKER)
        if idx != -1:
            return json.loads(text[idx + len(JSON_MARKER):].strip())
    raise RuntimeError(f"Editor command produced no result payload:\n{format_output(result)}")


def eval_statement(statement):
    """Evaluate a single expression in the editor and return its repr string."""
    result = run_python(statement, exec_mode=remote.MODE_EVAL_STATEMENT)
    if not result.get("success"):
        raise RuntimeError(format_output(result))
    return result.get("result")

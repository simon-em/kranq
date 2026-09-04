#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

PROTOCOL_VERSION = "2025-06-18"
SERVER_INFO = {"name": "bitbucket", "version": "1.0.0"}
API = "https://api.bitbucket.org/2.0"


TOKEN_KEYS = ("BITBUCKET_TOKEN", "BITBUCKET_API_TOKEN", "BITBUCKET_STEP_OAUTH_TOKEN")


def token() -> str:
    for key in TOKEN_KEYS:
        value = os.environ.get(key)
        if value:
            return value
    raise RuntimeError("none of " + ", ".join(TOKEN_KEYS) + " is set in the environment")


def workspace() -> str:
    return os.environ.get("BITBUCKET_WORKSPACE", "effetmonstre")


def repo() -> str:
    value = os.environ.get("BITBUCKET_REPO_SLUG")
    if not value:
        raise RuntimeError("BITBUCKET_REPO_SLUG is not set")
    return value


def default_pr() -> str | None:
    return os.environ.get("BITBUCKET_PR_ID")


def request(method: str, path: str, body: dict | None = None, raw: bool = False):
    url = f"{API}/{path}"
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Authorization": f"Bearer {token()}"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=45) as resp:
            payload = resp.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode(errors="replace")[:1500]
        raise RuntimeError(f"bitbucket {method} {path} failed with {exc.code}: {detail}") from None
    if raw:
        return payload.decode(errors="replace")
    return json.loads(payload) if payload else {}


def resolve_pr(arguments: dict) -> str:
    pr = str(arguments.get("pull_request_id") or default_pr() or "").strip()
    if not pr:
        raise RuntimeError("no pull_request_id given and BITBUCKET_PR_ID is not set")
    return pr


def tool_get_pull_request(arguments: dict) -> str:
    pr = resolve_pr(arguments)
    data = request("GET", f"repositories/{workspace()}/{repo()}/pullrequests/{pr}")
    summary = {
        "id": data.get("id"),
        "title": data.get("title"),
        "state": data.get("state"),
        "author": (data.get("author") or {}).get("display_name"),
        "source": ((data.get("source") or {}).get("branch") or {}).get("name"),
        "destination": ((data.get("destination") or {}).get("branch") or {}).get("name"),
        "description": (data.get("description") or "")[:4000],
        "link": ((data.get("links") or {}).get("html") or {}).get("href"),
    }
    return json.dumps(summary, indent=2)


def tool_get_pull_request_diff(arguments: dict) -> str:
    pr = resolve_pr(arguments)
    limit = int(arguments.get("max_bytes") or 120000)
    diff = request("GET", f"repositories/{workspace()}/{repo()}/pullrequests/{pr}/diff", raw=True)
    if len(diff) > limit:
        return diff[:limit] + f"\n\n[diff truncated at {limit} bytes]"
    return diff


def tool_list_comments(arguments: dict) -> str:
    pr = resolve_pr(arguments)
    path = f"repositories/{workspace()}/{repo()}/pullrequests/{pr}/comments?pagelen=100"
    out = []
    while path:
        page = request("GET", path)
        for comment in page.get("values", []):
            if comment.get("deleted"):
                continue
            entry = {
                "id": comment.get("id"),
                "author": (comment.get("user") or {}).get("display_name"),
                "created_on": comment.get("created_on"),
                "text": ((comment.get("content") or {}).get("raw") or "")[:2000],
            }
            inline = comment.get("inline")
            if inline:
                entry["file"] = inline.get("path")
                entry["line"] = inline.get("to") or inline.get("from")
            out.append(entry)
        nxt = page.get("next")
        path = nxt.split(f"{API}/", 1)[1] if nxt else None
    return json.dumps(out, indent=2)


def tool_add_comment(arguments: dict) -> str:
    pr = resolve_pr(arguments)
    text = (arguments.get("text") or "").strip()
    if not text:
        raise RuntimeError("text is required")
    body: dict = {"content": {"raw": text}}
    path = arguments.get("file")
    line = arguments.get("line")
    if path:
        inline: dict = {"path": path}
        if line is not None:
            inline["to"] = int(line)
        body["inline"] = inline
    data = request("POST", f"repositories/{workspace()}/{repo()}/pullrequests/{pr}/comments", body)
    return json.dumps(
        {"id": data.get("id"), "link": ((data.get("links") or {}).get("html") or {}).get("href")},
        indent=2,
    )


def tool_list_open_pull_requests(arguments: dict) -> str:
    limit = int(arguments.get("limit") or 25)
    data = request("GET", f"repositories/{workspace()}/{repo()}/pullrequests?state=OPEN&pagelen={limit}")
    out = [
        {
            "id": pr.get("id"),
            "title": pr.get("title"),
            "author": (pr.get("author") or {}).get("display_name"),
            "source": ((pr.get("source") or {}).get("branch") or {}).get("name"),
        }
        for pr in data.get("values", [])
    ]
    return json.dumps(out, indent=2)


PR_ARG = {
    "pull_request_id": {
        "type": "string",
        "description": "Pull request id. Defaults to BITBUCKET_PR_ID when omitted.",
    }
}

TOOLS = [
    {
        "name": "list_open_pull_requests",
        "description": "List the open pull requests in the repository.",
        "inputSchema": {
            "type": "object",
            "properties": {"limit": {"type": "integer", "description": "How many to return, default 25."}},
        },
        "handler": tool_list_open_pull_requests,
    },
    {
        "name": "get_pull_request",
        "description": "Get the title, author, branches, description and link of a pull request.",
        "inputSchema": {"type": "object", "properties": dict(PR_ARG)},
        "handler": tool_get_pull_request,
    },
    {
        "name": "get_pull_request_diff",
        "description": "Get the unified diff of a pull request.",
        "inputSchema": {
            "type": "object",
            "properties": dict(PR_ARG, max_bytes={"type": "integer", "description": "Truncate the diff at this many bytes, default 120000."}),
        },
        "handler": tool_get_pull_request_diff,
    },
    {
        "name": "list_pull_request_comments",
        "description": "List existing comments on a pull request, so you do not repeat feedback that has already been given.",
        "inputSchema": {"type": "object", "properties": dict(PR_ARG)},
        "handler": tool_list_comments,
    },
    {
        "name": "add_pull_request_comment",
        "description": "Post a comment on a pull request. Give file and line to attach it to a specific line of the diff, otherwise it is posted as a general comment.",
        "inputSchema": {
            "type": "object",
            "properties": dict(
                PR_ARG,
                text={"type": "string", "description": "Comment body, markdown is supported."},
                file={"type": "string", "description": "Path to attach the comment to, for an inline comment."},
                line={"type": "integer", "description": "Line number in the new version of the file."},
            ),
            "required": ["text"],
        },
        "handler": tool_add_comment,
    },
]

HANDLERS = {tool["name"]: tool["handler"] for tool in TOOLS}
TOOL_SPECS = [{k: v for k, v in tool.items() if k != "handler"} for tool in TOOLS]


def respond(message_id, result=None, error=None) -> None:
    message = {"jsonrpc": "2.0", "id": message_id}
    if error is not None:
        message["error"] = error
    else:
        message["result"] = result
    sys.stdout.write(json.dumps(message) + "\n")
    sys.stdout.flush()


def handle(message: dict) -> None:
    method = message.get("method")
    message_id = message.get("id")

    if method == "initialize":
        requested = (message.get("params") or {}).get("protocolVersion") or PROTOCOL_VERSION
        respond(message_id, {
            "protocolVersion": requested,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": SERVER_INFO,
        })
        return

    if method in ("notifications/initialized", "notifications/cancelled"):
        return

    if method == "ping":
        respond(message_id, {})
        return

    if method == "tools/list":
        respond(message_id, {"tools": TOOL_SPECS})
        return

    if method == "tools/call":
        params = message.get("params") or {}
        name = params.get("name")
        handler = HANDLERS.get(name)
        if handler is None:
            respond(message_id, error={"code": -32602, "message": f"unknown tool {name}"})
            return
        try:
            text = handler(params.get("arguments") or {})
            respond(message_id, {"content": [{"type": "text", "text": text}], "isError": False})
        except Exception as exc:
            respond(message_id, {"content": [{"type": "text", "text": str(exc)}], "isError": True})
        return

    if message_id is not None:
        respond(message_id, error={"code": -32601, "message": f"unknown method {method}"})


def main() -> int:
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError:
            continue
        handle(message)
    return 0


if __name__ == "__main__":
    sys.exit(main())

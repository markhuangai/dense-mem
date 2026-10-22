import asyncio
import json
import socket
import threading
import time
from contextlib import asynccontextmanager

import pytest
import uvicorn
from mcp.server.lowlevel import Server
from mcp.server.streamable_http_manager import StreamableHTTPSessionManager
from mcp.types import CallToolResult, TextContent, Tool
from starlette.applications import Starlette
from starlette.responses import JSONResponse, Response
from starlette.routing import Mount


@pytest.fixture
def wire_server():
    state = {
        "requests": [],
        "calls": [],
        "called": threading.Event(),
        "result": {"processing_state": "completed"},
    }
    server = Server("densemem-client-test")

    @server.list_tools()
    async def tools():
        return [
            Tool(
                name=name,
                inputSchema={"type": "object"},
                outputSchema={"type": "object"},
            )
            for name in ("remember", "ingest_session", "recall_memory", "trace_memory")
        ]

    @server.call_tool()
    async def call(name, arguments):
        state["calls"].append((name, arguments))
        state["called"].set()
        if state.get("delay"):
            await asyncio.sleep(state["delay"])
        result = (
            state["result_factory"](name, arguments)
            if "result_factory" in state
            else state["result"]
        )
        if state.get("handler"):
            result = await state["handler"](name, arguments)
        return CallToolResult(
            structuredContent=result,
            content=[TextContent(type="text", text=json.dumps(result))],
            isError=state.get("is_error", False),
        )

    manager = StreamableHTTPSessionManager(server, json_response=True, stateless=True)

    class Endpoint:
        async def __call__(self, scope, receive, send):
            if state.get("status"):
                await JSONResponse(
                    state.get("http_error", {"error": "unauthorized"}),
                    state["status"],
                    headers=state.get("http_headers"),
                )(scope, receive, send)
                return
            if state.get("redirect"):
                await Response(status_code=307, headers={"Location": state["redirect"]})(
                    scope, receive, send
                )
                return
            if state.get("malformed"):
                await Response(b"invalid-json", media_type="application/json")(scope, receive, send)
                return
            body = b""
            while True:
                event = await receive()
                body += event.get("body", b"")
                if not event.get("more_body"):
                    break
            if body:
                headers = dict(scope["headers"])
                state["requests"].append((headers.get(b"authorization"), json.loads(body)))
                status = state.get("status_after_initialize")
                if isinstance(status, dict):
                    status = status.get(headers.get(b"authorization"))
                if status and json.loads(body).get("method") != "initialize":
                    await JSONResponse(
                        state["http_error"], status, headers=state.get("http_headers")
                    )(scope, receive, send)
                    return
            replayed = False

            async def replay():
                nonlocal replayed
                if not replayed:
                    replayed = True
                    return {"type": "http.request", "body": body}
                return await receive()

            await manager.handle_request(scope, replay, send)

    @asynccontextmanager
    async def lifespan(app):
        async with manager.run():
            yield

    app = Starlette(routes=[Mount("/mcp", app=Endpoint())], lifespan=lifespan)
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    http = uvicorn.Server(uvicorn.Config(app, log_level="error", timeout_graceful_shutdown=0))
    thread = threading.Thread(target=lambda: http.run(sockets=[sock]), daemon=True)
    thread.start()
    deadline = time.monotonic() + 5
    while not http.started and time.monotonic() < deadline:
        time.sleep(0.01)
    assert http.started, "official SDK test server did not start"
    yield f"http://127.0.0.1:{port}/mcp/", state
    http.should_exit = True
    thread.join(timeout=5)
    assert not thread.is_alive(), "official SDK test server did not stop"

"""End-to-end smoke test: spawns the MCP server over stdio (like a real MCP
client would), lists tools, and exercises a few against a running editor.

Usage: .venv/Scripts/python.exe smoke_test.py [--live]
Without --live it only verifies the server starts and lists tools.
"""
import asyncio
import sys
from pathlib import Path

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

SERVER_DIR = Path(__file__).parent


async def main(live: bool):
    params = StdioServerParameters(
        command=str(SERVER_DIR / ".venv" / "Scripts" / "python.exe"),
        args=[str(SERVER_DIR / "server.py")],
    )
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            tools = await session.list_tools()
            names = [t.name for t in tools.tools]
            print(f"OK: server started, {len(names)} tools: {', '.join(names)}")

            if not live:
                return

            result = await session.call_tool("editor_status", {})
            print("editor_status:", result.content[0].text if result.content else result.structuredContent)
            if result.isError:
                raise SystemExit("editor_status failed - is the editor running?")

            result = await session.call_tool("execute_python", {"code": "21 * 2", "evaluate": True})
            value = result.content[0].text
            assert value == "42", f"expected 42, got {value!r}"
            print("execute_python eval: 21 * 2 =", value)

            result = await session.call_tool("list_actors", {"name_filter": "EnemySpawn"})
            print("list_actors(EnemySpawn):", result.content[0].text[:300])

            print("LIVE SMOKE TEST PASSED")


if __name__ == "__main__":
    asyncio.run(main("--live" in sys.argv))

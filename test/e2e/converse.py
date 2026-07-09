"""Minimal websocket converse check for the e2e (SP2).

Connects to an agent facade, sends one message, and asserts a non-error
response. Success = a `connected` frame plus at least one response frame
(chunk/message/done) and no `error` frame. Env: AGENT, NS.
"""
import asyncio
import json
import os
import sys

import websockets

AGENT = os.environ["AGENT"]
NS = os.environ["NS"]
# Agents are data-plane-auth-gated by default; send the shared bearer token.
TOKEN = os.environ.get("TOKEN", "")
URI = f"ws://{AGENT}.{NS}.svc.cluster.local:8080/ws?agent={AGENT}"


async def run() -> None:
    print(f"connecting: {URI}")
    headers = {"Authorization": f"Bearer {TOKEN}"} if TOKEN else {}
    async with websockets.connect(URI, additional_headers=headers, ping_interval=None) as ws:
        await ws.send(json.dumps({"type": "message", "content": "Hello, are you there?"}))
        connected = False
        responded = False
        for _ in range(20):
            try:
                msg = json.loads(await asyncio.wait_for(ws.recv(), timeout=30))
            except asyncio.TimeoutError:
                break
            kind = msg.get("type")
            print(f"recv: {kind}")
            if kind == "connected":
                connected = True
            elif kind in ("chunk", "message", "done"):
                responded = True
            elif kind == "error":
                print(f"AGENT ERROR: {msg.get('error')}")
                sys.exit(1)
            if kind == "done":
                break
        if not connected:
            print("FAIL: never received a 'connected' frame")
            sys.exit(1)
        if not responded:
            print("FAIL: agent produced no response")
            sys.exit(1)
        print(f"CONVERSE OK: {AGENT} answered without error")


asyncio.run(run())

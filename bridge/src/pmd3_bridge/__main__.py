# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Entrypoint: python -m pmd3_bridge --socket <path>

Binds uvicorn to a Unix domain socket.  After the socket is listening,
writes "ready\n" to stdout so the parent process (spyder's Go daemon)
knows the bridge is up.  Blocks until SIGTERM, then shuts down gracefully.
"""
from __future__ import annotations

import argparse
import asyncio
import os
import signal
import sys


def _parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        prog="pmd3_bridge",
        description="HTTP bridge for pymobiledevice3 over a Unix domain socket",
    )
    p.add_argument(
        "--socket",
        required=True,
        metavar="PATH",
        help="Path for the Unix domain socket to bind",
    )
    return p.parse_args()


def _validate_socket_path(socket_path: str) -> None:
    parent = os.path.dirname(os.path.abspath(socket_path))
    if not os.path.isdir(parent):
        print(f"error: socket parent directory does not exist: {parent}", file=sys.stderr)
        sys.exit(1)
    if not os.access(parent, os.W_OK):
        print(f"error: socket parent directory is not writable: {parent}", file=sys.stderr)
        sys.exit(1)
    if os.path.exists(socket_path):
        # A leftover socket from a previous run is fine — we'll overwrite it.
        # A regular file or directory is not.
        if not os.path.islink(socket_path) and os.stat(socket_path).st_mode & 0o170000 != 0o140000:
            print(f"error: socket path already exists and is not a socket: {socket_path}", file=sys.stderr)
            sys.exit(1)


def main() -> None:
    args = _parse_args()
    socket_path: str = args.socket

    _validate_socket_path(socket_path)

    # Remove a stale socket file so uvicorn can bind cleanly.
    if os.path.exists(socket_path):
        os.unlink(socket_path)

    import uvicorn
    from .app import app

    config = uvicorn.Config(
        app,
        uds=socket_path,
        log_level="info",
    )
    server = uvicorn.Server(config)

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)

    # Replace uvicorn's default signal handlers so we can emit "ready\n" after
    # the server is actually listening.
    shutdown_event = asyncio.Event()

    def _handle_sigterm(*_) -> None:  # type: ignore[override]
        shutdown_event.set()

    async def _run() -> None:
        # Install SIGTERM handler inside the event loop.
        loop.add_signal_handler(signal.SIGTERM, _handle_sigterm)

        # Disable uvicorn's own signal handling so we control shutdown.
        server.install_signal_handlers = lambda: None  # type: ignore[method-assign]

        serve_task = loop.create_task(server.serve())

        # Wait until uvicorn is actually listening.
        while not server.started:
            await asyncio.sleep(0.05)

        # Signal to the parent that we are ready.
        sys.stdout.write("ready\n")
        sys.stdout.flush()

        # Block until SIGTERM arrives.
        await shutdown_event.wait()

        # Graceful shutdown.
        server.should_exit = True
        await serve_task

    try:
        loop.run_until_complete(_run())
    finally:
        loop.close()


if __name__ == "__main__":
    main()

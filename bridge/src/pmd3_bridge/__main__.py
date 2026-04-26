# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Entrypoint: python -m pmd3_bridge

Binds Uvicorn to an ephemeral loopback port on 127.0.0.1, generates a
fresh cryptographically-random bearer token, and once the server is
actually listening writes a single structured line to stdout:

    ready port=<NNNN> token=<BASE64>\n

The spyder Go daemon reads that line, constructs an HTTP client pointing
at http://127.0.0.1:<NNNN> with `Authorization: Bearer <token>` on every
request, and never shares either value with any other process.

The bridge is a private subprocess of the daemon (🎯T26.1): no
filesystem socket, no fixed port, no discoverability from outside the
daemon. External processes cannot hit the bridge even if they know the
host — the port is ephemeral and the token is unguessable.

Logging (🎯T26.5): configures root + pymobiledevice3 + uvicorn loggers
to the level named by SPYDER_LOG_LEVEL (default INFO), routed to stderr
so the daemon's launchd log captures everything.
"""
from __future__ import annotations

import asyncio
import logging
import os
import secrets
import signal
import socket
import sys
import threading


def _configure_logging() -> str:
    """Configure loggers to emit to stderr. Returns the chosen level string."""
    level_name = os.environ.get("SPYDER_LOG_LEVEL", "INFO").upper()
    level = getattr(logging, level_name, logging.INFO)

    fmt = "%(asctime)s %(levelname)s %(name)s %(message)s"
    logging.basicConfig(
        level=level,
        format=fmt,
        stream=sys.stderr,
        force=True,
    )

    for name in ("pymobiledevice3", "pmd3_bridge", "uvicorn", "uvicorn.error",
                 "uvicorn.access", "fastapi"):
        logging.getLogger(name).setLevel(level)

    return level_name.lower()


def _bind_ephemeral_loopback() -> tuple[socket.socket, int]:
    """Bind a TCP socket on 127.0.0.1:0 and return (sock, port).

    Binding up front lets us know the port before Uvicorn starts, so the
    `ready` line can be written as soon as the server accepts the first
    connection. Uvicorn is handed the already-bound socket.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    return s, port


def _watch_parent_via_stdin() -> None:
    """Watch fd 0 for EOF; when the parent's write end closes, exit immediately.

    🎯T44: the spyder Go-side supervisor passes the read end of an os.Pipe()
    as our stdin (fd 0) and holds the write end open for its lifetime.
    Whenever the parent process exits — clean shutdown, panic, SIGKILL,
    OOM, anything — the kernel closes its copy of the write end, our
    read returns EOF, and we self-exit. This is the kernel-guaranteed
    parent-liveness signal that prevents orphaned pmd3-bridge processes
    after a parent panic. See 🎯T44 in bullseye.yaml.

    When stdin is /dev/null (e.g. someone runs pmd3-bridge directly
    without the supervisor), os.read(0, 1) returns b'' immediately and
    we exit cleanly — fine for that debug case. When stdin is a tty
    (interactive shell), the read blocks until the operator types or
    closes their shell (which sends SIGHUP); also fine.
    """
    try:
        os.read(0, 1)
    except OSError:
        # EBADF or similar — stdin not available. Don't kill the process
        # over diagnostic plumbing.
        return
    # EOF or one byte read — either way the parent is gone or stdin was
    # not a long-lived pipe. Exit immediately; bypass atexit handlers
    # since the parent is already dead and any cleanup involving it
    # (e.g. RPC to spyder) would just hang.
    os._exit(0)


def main() -> None:
    log_level = _configure_logging()
    log = logging.getLogger("pmd3_bridge")

    # Start the parent-liveness watcher before anything else. If the parent
    # (spyder daemon) dies for any reason, stdin-EOF fires _exit(0) here.
    # daemon=True so this thread does not keep the process alive on its own.
    watcher = threading.Thread(target=_watch_parent_via_stdin, daemon=True, name="stdin-eof-watcher")
    watcher.start()

    listener, port = _bind_ephemeral_loopback()
    token = secrets.token_urlsafe(32)
    log.info("pmd3-bridge starting port=%d log_level=%s pid=%s",
             port, log_level, os.getpid())

    import uvicorn
    from pmd3_bridge.app import app, set_auth_token, _set_services

    # Install the token into the app so the auth middleware can validate it.
    set_auth_token(token)

    # Developer escape hatch for no-device integration testing (🎯T26.4):
    # substitute the fakes module for the real pmd3-backed services. This
    # is the ONLY fake surviving the mock retirement and it exists because
    # pmd3 requires a paired device, not because we simulate misbehaviour.
    if os.environ.get("SPYDER_BRIDGE_FAKE_SERVICES") == "1":
        from pmd3_bridge import _fake_services
        _set_services(_fake_services)
        log.warning("pmd3-bridge: SPYDER_BRIDGE_FAKE_SERVICES=1 — pmd3 is stubbed")

    config = uvicorn.Config(
        app,
        log_level=log_level,
        access_log=True,
    )
    server = uvicorn.Server(config)

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)

    shutdown_event = asyncio.Event()

    def _handle_sigterm(*_) -> None:  # type: ignore[override]
        log.info("pmd3-bridge received SIGTERM, shutting down")
        shutdown_event.set()

    async def _run() -> None:
        loop.add_signal_handler(signal.SIGTERM, _handle_sigterm)
        server.install_signal_handlers = lambda: None  # type: ignore[method-assign]

        # Hand the pre-bound listener to uvicorn via config.bind_socket.
        # Uvicorn's lifespan machinery adopts the existing socket.
        serve_task = loop.create_task(server.serve(sockets=[listener]))

        while not server.started:
            await asyncio.sleep(0.05)

        log.info("pmd3-bridge listening port=%d", port)
        # Structured handshake: port + token, single line, stdout.
        sys.stdout.write(f"ready port={port} token={token}\n")
        sys.stdout.flush()

        await shutdown_event.wait()

        log.info("pmd3-bridge draining")
        server.should_exit = True
        await serve_task
        log.info("pmd3-bridge drained cleanly")

    try:
        loop.run_until_complete(_run())
    finally:
        loop.close()


if __name__ == "__main__":
    main()

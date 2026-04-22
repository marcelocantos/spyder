# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Entrypoint: python -m pmd3_bridge --socket <path>

Binds uvicorn to a Unix domain socket.  After the socket is listening,
writes "ready\n" to stdout so the parent process (spyder's Go daemon)
knows the bridge is up.  Blocks until SIGTERM, then shuts down gracefully.

Logging (🎯T26.5): the bridge configures the root Python logger, the
pymobiledevice3 logger, and Uvicorn's access/error loggers all to the
same level (default INFO, override via SPYDER_LOG_LEVEL). Every log
record routes to stderr, which the spyder daemon inherits so all
breadcrumbs land in the same file. Uvicorn's access log is enabled,
so every HTTP request from the daemon produces a line here.
"""
from __future__ import annotations

import argparse
import asyncio
import logging
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


def _configure_logging() -> str:
    """Configure root + pymobiledevice3 + uvicorn loggers to emit to stderr
    with timestamps. Returns the chosen log level string for downstream use.
    """
    level_name = os.environ.get("SPYDER_LOG_LEVEL", "INFO").upper()
    level = getattr(logging, level_name, logging.INFO)

    fmt = "%(asctime)s %(levelname)s %(name)s %(message)s"
    logging.basicConfig(
        level=level,
        format=fmt,
        stream=sys.stderr,
        force=True,  # override any prior configuration inherited from PyInstaller bundle
    )

    # pymobiledevice3 installs its own loggers; ensure they propagate at the
    # chosen level so USB/lockdown/AFC events appear in the trail.
    for name in ("pymobiledevice3", "pmd3_bridge", "uvicorn", "uvicorn.error",
                 "uvicorn.access", "fastapi"):
        logging.getLogger(name).setLevel(level)

    return level_name.lower()


def main() -> None:
    args = _parse_args()
    socket_path: str = args.socket

    _validate_socket_path(socket_path)

    log_level = _configure_logging()
    log = logging.getLogger("pmd3_bridge")
    log.info("pmd3-bridge starting socket=%s log_level=%s pid=%s",
             socket_path, log_level, os.getpid())

    # Remove a stale socket file so uvicorn can bind cleanly.
    if os.path.exists(socket_path):
        os.unlink(socket_path)

    import uvicorn
    # Import via the fully-qualified name so the PyInstaller entrypoint
    # (which invokes this module as `__main__` with no parent package)
    # resolves the same as `python -m pmd3_bridge` does in development.
    from pmd3_bridge.app import app

    config = uvicorn.Config(
        app,
        uds=socket_path,
        log_level=log_level,
        access_log=True,
    )
    server = uvicorn.Server(config)

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)

    # Replace uvicorn's default signal handlers so we can emit "ready\n" after
    # the server is actually listening.
    shutdown_event = asyncio.Event()

    def _handle_sigterm(*_) -> None:  # type: ignore[override]
        log.info("pmd3-bridge received SIGTERM, shutting down")
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

        log.info("pmd3-bridge listening socket=%s", socket_path)

        # Signal to the parent that we are ready.
        sys.stdout.write("ready\n")
        sys.stdout.flush()

        # Block until SIGTERM arrives.
        await shutdown_event.wait()

        # Graceful shutdown.
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

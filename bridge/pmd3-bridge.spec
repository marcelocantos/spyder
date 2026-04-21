# pmd3-bridge.spec — PyInstaller onedir spec for arm64 macOS
#
# Hidden imports rationale:
#
#   pymobiledevice3 uses entry-point-based service discovery and imports many
#   sub-packages lazily at runtime.  PyInstaller's static analyser misses them.
#
#   cryptography  — pmd3 uses hazmat primitives imported by string; the backend
#                   and binding modules must be explicitly included.
#   construct      — used by pmd3 for binary protocol parsing; many submodules
#                   are dynamically imported.
#   cffi / _cffi_backend — required by cryptography on arm64.
#   zeroconf       — used by pmd3's tunnel/service-discovery layer.
#   uvicorn        — some sub-modules (logging_config, protocols.*) are
#                   dynamically imported by the server startup.
#   fastapi        — background-task and exception-handler internals.
#   anyio          — asyncio backend used by starlette/fastapi.
#   h11            — HTTP/1.1 codec used by uvicorn.
#
# datas:
#   pymobiledevice3 ships several plist/json resource files that are loaded
#   at runtime via importlib.resources.  We must carry them into the bundle.
#
# Platform note: arm64-only.  Never add x86_64 target_arch entries here.

import sys
from pathlib import Path
import pymobiledevice3

PMD3_PKG = Path(pymobiledevice3.__file__).parent

a = Analysis(
    ['src/pmd3_bridge/__main__.py'],
    pathex=[],
    binaries=[],
    datas=[
        # pymobiledevice3 resource files (plist, json, entitlements, etc.)
        (str(PMD3_PKG), 'pymobiledevice3'),
    ],
    hiddenimports=[
        # pymobiledevice3 service modules (dynamically imported)
        'pymobiledevice3.services.power_assertion',
        'pymobiledevice3.services.installation_proxy',
        'pymobiledevice3.services.dvt.dvt_secure_socket_proxy',
        'pymobiledevice3.services.dvt.instruments.process_control',
        'pymobiledevice3.services.dvt.instruments.device_info',
        'pymobiledevice3.services.diagnostics',
        'pymobiledevice3.services.screenshot',
        'pymobiledevice3.services.crash_reports',
        'pymobiledevice3.lockdown',
        'pymobiledevice3.usbmux',
        # cryptography backend
        'cryptography.hazmat.primitives',
        'cryptography.hazmat.backends',
        'cryptography.hazmat.backends.openssl',
        'cryptography.hazmat.backends.openssl.backend',
        'cryptography.hazmat.bindings._rust',
        # cffi
        'cffi',
        '_cffi_backend',
        # construct
        'construct',
        'construct.lib',
        'construct.lib.containers',
        # zeroconf (pmd3 tunnel discovery)
        'zeroconf',
        'zeroconf._utils',
        'zeroconf._services',
        # uvicorn internals
        'uvicorn.logging',
        'uvicorn.loops',
        'uvicorn.loops.asyncio',
        'uvicorn.loops.uvloop',
        'uvicorn.protocols',
        'uvicorn.protocols.http',
        'uvicorn.protocols.http.h11_impl',
        'uvicorn.protocols.http.httptools_impl',
        'uvicorn.protocols.websockets',
        'uvicorn.protocols.websockets.websockets_impl',
        'uvicorn.lifespan',
        'uvicorn.lifespan.on',
        # anyio / starlette async backend
        'anyio',
        'anyio._backends._asyncio',
        # h11
        'h11',
        # fastapi
        'fastapi.background',
        'fastapi.exception_handlers',
    ],
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
)

pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name='pmd3-bridge',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,
    console=True,
    target_arch='arm64',
)

coll = COLLECT(
    exe,
    a.binaries,
    a.datas,
    strip=False,
    upx=False,
    upx_exclude=[],
    name='pmd3-bridge',
)

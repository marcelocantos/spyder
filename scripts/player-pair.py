#!/usr/bin/env python3
"""Print a LAN browser-player link and optionally render its QR as a PNG.

The daemon must be reachable from the scanning device and have the optional
wasm bundle installed. This does not change native player discovery/Keychain.
"""
import argparse
import re
import shutil
import subprocess
import sys
from urllib.parse import urlencode, urlunsplit


def pairing_url(host: str, port: int, name: str) -> str:
    if not host or any(c in host for c in '/?#@\\ \t\r\n'):
        raise ValueError('host must be a hostname or IP address, not a URL')
    if ':' in host or '[' in host or ']' in host:
        raise ValueError('IPv6 is not supported by the browser player WebSocket URL yet')
    if not 1 <= port <= 65535:
        raise ValueError('port must be 1–65535')
    if not re.fullmatch(r'[A-Za-z0-9._-]+', name):
        raise ValueError('name must contain only letters, digits, dots, underscores or hyphens (wire query safety)')
    return urlunsplit(('http', f'{host}:{port}', '/player/', urlencode({'host': host, 'port': port, 'name': name}), ''))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', required=True, help='LAN-reachable daemon hostname or IP (not localhost on a phone)')
    parser.add_argument('--port', type=int, default=3030)
    parser.add_argument('--name', required=True, help='streaming server catalogue name')
    parser.add_argument('--qr', metavar='PNG', help='write QR PNG via the optional qrencode CLI')
    args = parser.parse_args()
    try:
        url = pairing_url(args.host, args.port, args.name)
    except ValueError as exc:
        parser.error(str(exc))
    if args.qr:
        if not shutil.which('qrencode'):
            parser.error('qrencode is required for --qr (install it or share the printed URL)')
        subprocess.run(['qrencode', '-o', args.qr, url], check=True)
    print(url)
    if args.host in ('localhost', '127.0.0.1'):
        print('warning: localhost points to the phone, not your daemon; use its LAN address', file=sys.stderr)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

#!/usr/bin/env python3
"""Hermetic URL + QR command test; no camera/network/daemon required."""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit

SCRIPT = Path(__file__).with_name('player-pair.py')
spec = importlib.util.spec_from_file_location('pair', SCRIPT)
pair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pair)


class PairTest(unittest.TestCase):
    def test_same_origin_and_encoded_name(self):
        u = urlsplit(pair.pairing_url('192.168.1.23', 3030, 'game-test_2'))
        self.assertEqual((u.scheme, u.netloc, u.path), ('http', '192.168.1.23:3030', '/player/'))
        self.assertEqual(parse_qs(u.query), {'host': ['192.168.1.23'], 'port': ['3030'], 'name': ['game-test_2']})

    def test_invalid(self):
        for h in ('', 'https://evil.test', 'host/path', 'foo?bar', 'foo@bar', '2001:db8::1'):
            with self.subTest(host=h), self.assertRaises(ValueError):
                pair.pairing_url(h, 3030, 'game')
        for p in (0, 65536):
            with self.assertRaises(ValueError):
                pair.pairing_url('host', p, 'game')
        for name in ('a/b', 'two words', 'a&b'):
            with self.assertRaises(ValueError):
                pair.pairing_url('host', 3030, name)

    def test_qr_invoked_with_exact_url(self):
        with tempfile.TemporaryDirectory() as d:
            exe = Path(d) / 'qrencode'
            exe.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$QR_ARGS"\n')
            exe.chmod(0o755)
            args = Path(d) / 'args'
            png = Path(d) / 'link.png'
            env = dict(os.environ, PATH=d + os.pathsep + os.environ['PATH'], QR_ARGS=str(args))
            r = subprocess.run([sys.executable, str(SCRIPT), '--host', 'lan.test', '--name', 'two-words', '--qr', str(png)], env=env, text=True, capture_output=True, check=True)
            self.assertEqual(args.read_text().splitlines(), ['-o', str(png), r.stdout.strip()])


if __name__ == '__main__':
    unittest.main()

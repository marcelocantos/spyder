# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Real-codec migration regression, including publication recovery and corruption."""
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from PIL import Image

spec = importlib.util.spec_from_file_location("recompress", Path(__file__).with_name("recompress-run-archives.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class RecompressTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name)
        self.rid = "test-run"
        self.archive = self.directory / "test-run.tar.zst"
        self.manifest = self.directory / "test-run.manifest.json"
        png = self.directory / "screen.png"
        image = Image.new("RGB", (128, 96))
        image.putdata([(x * 3 % 256, y * 5 % 256, x * y % 256) for y in range(96) for x in range(128)])
        image.save(png)
        jxl = png.with_suffix(".jxl")
        migration.command("cjxl", "-d", "0", "-e", "7", str(png), str(jxl))
        self.entries = [{"name": "screen.png", "stored_as": "screen.jxl", "codec": migration.LOSSLESS,
                         "sha256": migration.digest(png), "size": png.stat().st_size}]
        self.contents = {"test-run/screen.jxl": jxl.read_bytes(), "test-run/log.txt": b"original log\n",
                         "test-run/archive-files.json": json.dumps({"files": self.entries}).encode()}
        raw = self.directory / "old.tar"
        with tarfile.open(raw, "w") as tf:
            for name, data in self.contents.items():
                header = tarfile.TarInfo(name)
                header.size = len(data)
                tf.addfile(header, io.BytesIO(data))
        migration.command("zstd", "-q", str(raw), "-o", str(self.archive))
        migration.save_json(self.manifest, {"run_id": self.rid, "files": self.entries,
                             "archive_bytes": self.archive.stat().st_size,
                             "archive_sha256": migration.digest(self.archive), "custom": "preserved"})
        self.old_archive = self.archive.read_bytes()
        self.old_manifest = self.manifest.read_bytes()

    def test_real_codecs_preserve_non_images_and_skip_repeat(self):
        result = migration.recompress(self.directory, self.manifest, 2)
        self.assertTrue(result["recompression"]["non_image_members_byte_identical"])
        self.assertEqual("preserved", result["custom"])
        current = json.loads(self.manifest.read_text())
        self.assertEqual(migration.CODEC, current["files"][0]["codec"])
        restored = self.directory / "restored"
        _, hashes = migration.read_tar(self.archive, restored)
        for name, content in self.contents.items():
            if not name.endswith(".jxl"):
                self.assertEqual(content, (restored / name).read_bytes())
        self.assertNotEqual(self.contents["test-run/screen.jxl"], (restored / "test-run/screen.jxl").read_bytes())
        self.assertIsNone(migration.recompress(self.directory, self.manifest, 2))
        self.assertEqual(1, len((self.directory / "index.jsonl").read_text().splitlines()))

    def test_changed_non_image_rejected_before_publish(self):
        real_write = migration.write_tar
        def corrupt(destination, headers, root):
            (root / "test-run/log.txt").write_text("corrupted")
            real_write(destination, headers, root)
        with patch.object(migration, "write_tar", corrupt):
            with self.assertRaisesRegex(ValueError, "replacement members/content differ"):
                migration.recompress(self.directory, self.manifest, 2)
        self.assertEqual(self.old_archive, self.archive.read_bytes())
        self.assertEqual(self.old_manifest, self.manifest.read_bytes())
        self.assertFalse((self.directory / "index.jsonl").exists())

    def test_interrupted_publish_resumes_verified_archive(self):
        with patch.object(migration, "finish_publication", side_effect=RuntimeError("interrupted")):
            with self.assertRaisesRegex(RuntimeError, "interrupted"):
                migration.recompress(self.directory, self.manifest, 2)
        transaction, = (self.directory / ".recompress").iterdir()
        self.assertEqual(self.old_archive, self.archive.read_bytes())
        # Simulate interruption after archive rename but before sidecar publication.
        (transaction / "new.tar.zst").replace(self.archive)
        migration.finish_publication(self.directory, transaction)
        self.assertEqual(migration.CODEC, json.loads(self.manifest.read_text())["files"][0]["codec"])
        self.assertFalse(transaction.exists())

    def test_corrupt_replacement_never_published(self):
        with patch.object(migration, "write_tar", side_effect=lambda dst, *_: dst.write_bytes(b"corrupt")):
            with self.assertRaises(migration.subprocess.CalledProcessError):
                migration.recompress(self.directory, self.manifest, 2)
        self.assertEqual(self.old_archive, self.archive.read_bytes())
        self.assertEqual(self.old_manifest, self.manifest.read_bytes())

    def test_unsafe_tar_path_is_rejected(self):
        raw = self.directory / "unsafe.tar"
        with tarfile.open(raw, "w") as tf:
            member = tarfile.TarInfo("../escape")
            member.size = 1
            tf.addfile(member, io.BytesIO(b"x"))
        unsafe = self.directory / "unsafe.tar.zst"
        migration.command("zstd", "-q", str(raw), "-o", str(unsafe))
        with self.assertRaisesRegex(ValueError, "unsafe"):
            migration.read_tar(unsafe, self.directory / "out")
        self.assertFalse((self.directory / "escape").exists())

    def test_corrupt_original_never_replaced(self):
        self.archive.write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "original archive checksum mismatch"):
            migration.recompress(self.directory, self.manifest, 2)
        self.assertEqual(b"corrupt", self.archive.read_bytes())
        self.assertEqual(self.old_manifest, self.manifest.read_bytes())


if __name__ == "__main__":
    unittest.main()

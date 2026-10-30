#!/usr/bin/env python3
# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Recompress historical lossless JXL runs; preview unless --apply is supplied.

The external manifest is authoritative after migration. Embedded archive-files.json
is preserved as original provenance, like every other non-image tar member.
Interrupted publication resumes from a journal with hard-linked originals.
"""
import argparse
import copy
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile
from concurrent.futures import ThreadPoolExecutor

from PIL import Image

DISTANCE = "3"
EFFORT = "7"
CODEC = f"jxl-lossy(cjxl -d {DISTANCE} -e {EFFORT})"
LOSSLESS = "jxl-lossless(cjxl -d 0 -e 7)"
FORMAT = "tar+zstd -10; PNG stored as lossy JPEG XL (distance 3, effort 7; decode-verified)"


def command(*args):
    return subprocess.run(args, check=True, capture_output=True, stdin=subprocess.DEVNULL)


def digest(path):
    with open(path, "rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def save_json(path, data):
    temporary = path.with_name(path.name + ".tmp")
    with open(temporary, "w") as f:
        os.chmod(temporary, 0o600)
        json.dump(data, f, indent=1)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temporary, path)


def read_tar(archive, dest=None):
    """Validate every member and hash its content, optionally extracting regular files."""
    headers, hashes = [], {}
    with subprocess.Popen(["zstd", "-dcq", str(archive)], stdout=subprocess.PIPE) as proc:
        try:
            with tarfile.open(fileobj=proc.stdout, mode="r|") as tf:
                for member in tf:
                    name = member.name
                    parts = PurePosixPath(name).parts
                    if (not parts or name.startswith("/") or ".." in parts
                            or name in hashes or not (member.isfile() or member.isdir())):
                        raise ValueError(f"unsafe, duplicate or unsupported member: {name}")
                    headers.append(member)
                    if member.isdir():
                        hashes[name] = None
                        continue
                    src = tf.extractfile(member)
                    h = hashlib.sha256()
                    if dest is not None:
                        target = dest.joinpath(*parts)
                        target.parent.mkdir(parents=True, exist_ok=True)
                        with open(target, "xb") as out:
                            for block in iter(lambda: src.read(1 << 20), b""):
                                h.update(block)
                                out.write(block)
                    else:
                        for block in iter(lambda: src.read(1 << 20), b""):
                            h.update(block)
                    hashes[name] = h.hexdigest()
            # Consume padding before waiting; zstd may still be writing it.
            with open(os.devnull, "wb") as sink:
                shutil.copyfileobj(proc.stdout, sink)
        except BaseException:
            proc.kill()
            raise
        finally:
            proc.stdout.close()
        if proc.wait() != 0:
            raise RuntimeError(f"zstd failed reading {archive}")
    return headers, hashes


def convert(path):
    with tempfile.TemporaryDirectory(prefix="codec-", dir=path.parent) as scratch:
        png = Path(scratch) / "decoded.png"
        new = Path(scratch) / "lossy.jxl"
        back = Path(scratch) / "verified.png"
        command("nice", "djxl", str(path), str(png), "--num_threads=2")
        with Image.open(png) as image:
            image.load()
            size = image.size
        command("nice", "cjxl", "-d", DISTANCE, "-e", EFFORT,
                "--num_threads=2", str(png), str(new))
        command("nice", "djxl", str(new), str(back), "--num_threads=2")
        with Image.open(back) as image:
            image.load()
            if image.size != size:
                raise ValueError(f"decoded dimensions differ: {path}")
        old_size, new_size = path.stat().st_size, new.stat().st_size
        os.replace(new, path)
        return old_size, new_size, digest(path)


def write_tar(destination, headers, root):
    with subprocess.Popen(["nice", "zstd", "-q", "-10", "-T4", "-o", str(destination)],
                          stdin=subprocess.PIPE) as proc:
        try:
            with tarfile.open(fileobj=proc.stdin, mode="w|") as tf:
                for original in headers:
                    header = copy.copy(original)
                    if header.isfile():
                        source = root / header.name
                        header.size = source.stat().st_size
                        # A PAX size would override the recompressed member's size.
                        header.pax_headers = {k: v for k, v in header.pax_headers.items() if k != "size"}
                        with open(source, "rb") as f:
                            tf.addfile(header, f)
                    else:
                        tf.addfile(header)
        except BaseException:
            proc.kill()
            raise
        finally:
            proc.stdin.close()
        if proc.wait() != 0:
            raise RuntimeError("zstd compression failed")


def finish_publication(directory, transaction):
    """Roll forward only our verified archive; refuse concurrent foreign changes."""
    journal = json.loads((transaction / "commit.json").read_text())
    rid = journal["run_id"]
    if Path(rid).name != rid:
        raise ValueError("invalid run id in journal")
    archive = directory / f"{rid}.tar.zst"
    manifest = directory / f"{rid}.manifest.json"
    current = digest(archive)
    if current == journal["old_sha256"]:
        candidate = transaction / "new.tar.zst"
        if digest(candidate) != journal["new_sha256"]:
            raise ValueError("candidate changed after verification")
        command("zstd", "-tq", str(candidate))
        os.replace(candidate, archive)
    elif current != journal["new_sha256"]:
        raise ValueError(f"archive changed outside migration: {rid}")
    record = journal["manifest"]
    temp_manifest = transaction / "publish.manifest.json"
    save_json(temp_manifest, record)
    os.replace(temp_manifest, manifest)
    index_record = {k: v for k, v in record.items() if k not in ("files", "run_manifest")}
    # Append-only index: the last record for a run is authoritative. Replaying
    # after a crash may append the same record; it never loses earlier history.
    with open(directory / "index.jsonl", "a") as f:
        f.write(json.dumps(index_record) + "\n")
        f.flush()
        os.fsync(f.fileno())
    shutil.rmtree(transaction)
    return index_record


def recompress(directory, manifest_path, workers):
    record = json.loads(manifest_path.read_text())
    rid = record["run_id"]
    if Path(rid).name != rid or manifest_path.name != f"{rid}.manifest.json":
        raise ValueError("manifest/run identity mismatch")
    entries = [e for e in record["files"] if e["codec"] == LOSSLESS]
    if not entries:
        return None
    archive = directory / f"{rid}.tar.zst"
    if archive.is_symlink() or manifest_path.is_symlink():
        raise ValueError("refusing symlink archive/manifest")
    old_sha = digest(archive)
    if old_sha != record["archive_sha256"]:
        raise ValueError(f"original archive checksum mismatch: {rid}")
    command("zstd", "-tq", str(archive))
    transactions = directory / ".recompress"
    transactions.mkdir(exist_ok=True, mode=0o700)
    transaction = Path(tempfile.mkdtemp(prefix=f"{rid}-", dir=transactions))
    published = False
    try:
        root = transaction / "members"
        root.mkdir()
        headers, before = read_tar(archive, root)
        names = [f"{rid}/{e['stored_as']}" for e in entries]
        if len(set(names)) != len(names) or any(name not in before or before[name] is None for name in names):
            raise ValueError("manifest does not match screenshot members")
        for entry in entries:
            if not entry["name"].lower().endswith(".png") or not entry["stored_as"].endswith(".jxl"):
                raise ValueError("unexpected lossless image member")
        expected = before.copy()
        with ThreadPoolExecutor(workers) as pool:
            for entry, name, result in zip(entries, names, pool.map(convert, (root / n for n in names))):
                old_size, new_size, sha = result
                entry.update(codec=CODEC, stored_bytes=new_size, previous_stored_bytes=old_size)
                expected[name] = sha
        candidate = transaction / "new.tar.zst"
        write_tar(candidate, headers, root)
        os.chmod(candidate, archive.stat().st_mode & 0o777)
        command("zstd", "-tq", str(candidate))
        after_headers, after = read_tar(candidate)
        if [(h.name, h.type) for h in after_headers] != [(h.name, h.type) for h in headers] or after != expected:
            raise ValueError("replacement members/content differ from verified inputs")
        # Non-image members, including embedded provenance, are byte-identical.
        if any(after[n] != h for n, h in before.items() if n not in names):
            raise ValueError("non-image member changed")
        old_size, new_size = archive.stat().st_size, candidate.stat().st_size
        new_sha = digest(candidate)
        record.update(archive_bytes=new_size, archive_sha256=new_sha,
                      archive_path=str(archive), manifest_path=str(manifest_path),
                      archive_format=FORMAT, archive_method="jxl+tar.zst")
        record["recompression"] = {
            "at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "codec": CODEC, "previous_archive_bytes": old_size, "archive_bytes": new_size,
            "saved_bytes": old_size - new_size, "previous_archive_sha256": old_sha,
            "decoded_images": len(entries), "zstd_test": True, "members_match": True,
            "non_image_members_byte_identical": True,
            "embedded_manifest": "historical provenance; this sidecar records current codecs",
        }
        verification = record.setdefault("verification", {})
        verification.update(pixel_verified_pngs=0, decoded_pngs=len(entries),
                            zstd_test=True, tar_list_ok=True, sha256_match=True,
                            entries_sha256_match=True)
        # Hard links retain originals until archive, sidecar and index publish.
        os.link(archive, transaction / "original.tar.zst")
        os.link(manifest_path, transaction / "original.manifest.json")
        with open(candidate, "rb") as f:
            os.fsync(f.fileno())
        save_json(transaction / "commit.json", {
            "run_id": rid, "old_sha256": old_sha, "new_sha256": new_sha, "manifest": record,
        })
        published = True  # recovery journal now owns cleanup, even on failure
        if digest(archive) != old_sha:
            raise ValueError("original changed during conversion")
        return finish_publication(directory, transaction)
    finally:
        if not published:
            shutil.rmtree(transaction)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive-dir", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--run", help="limit to one run id")
    parser.add_argument("--workers", type=int, default=4)
    args = parser.parse_args()
    if not 1 <= args.workers <= 16:
        parser.error("--workers must be between 1 and 16")
    directory = args.archive_dir.resolve(strict=True)
    for tool in ("cjxl", "djxl", "zstd"):
        if shutil.which(tool) is None:
            parser.error(f"{tool} is required; originals are untouched")
    with open(directory / ".archive.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        pending = sorted((directory / ".recompress").glob("*/commit.json"))
        if pending and not args.apply:
            print(f"{len(pending)} interrupted publications will resume with --apply", flush=True)
        for journal in pending if args.apply else []:
            record = finish_publication(directory, journal.parent)
            print(json.dumps({"resumed": record["run_id"]}), flush=True)
        candidates = []
        for path in directory.glob("*.manifest.json"):
            record = json.loads(path.read_text())
            if args.run and record.get("run_id") != args.run:
                continue
            if any(e["codec"] == LOSSLESS for e in record.get("files", [])):
                candidates.append((record["archive_bytes"], path))
        total_before = total_after = 0
        for _, path in sorted(candidates):
            if not args.apply:
                print(path.name, flush=True)
                continue
            result = recompress(directory, path, args.workers)
            if result:
                rec = result["recompression"]
                total_before += rec["previous_archive_bytes"]
                total_after += rec["archive_bytes"]
                print(json.dumps({"run_id": result["run_id"], **rec}), flush=True)
        print(json.dumps({"candidates": len(candidates), "before": total_before,
                          "after": total_after, "saved": total_before - total_after}), flush=True)


if __name__ == "__main__":
    main()

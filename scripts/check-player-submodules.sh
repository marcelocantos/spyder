#!/usr/bin/env bash
# Audit (default) or detach (--repair) the player's git submodule object stores.
# Repack while alternates are still readable: simply deleting an alternate can
# strand commits and make even `git status` fail.
set -euo pipefail

mode="${1:-}"
if [[ -n "$mode" && "$mode" != --repair ]]; then
    echo "usage: $0 [--repair]" >&2
    exit 2
fi
root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
common="$(cd "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)" && pwd -P)"
failed=0
while IFS= read -r path; do
    # Worktrees may not have the submodule checked out, but share the module
    # store with the main checkout. Inspect that store directly.
    store="$common/modules/$path"
    if [[ ! -d "$store/objects" ]]; then
        echo "missing player submodule store: $path (git submodule update --init $path)" >&2
        failed=1
        continue
    fi
    alternate="$store/objects/info/alternates"
    if [[ -e "$alternate" ]]; then
        echo "borrowed object store: $path: $(cat "$alternate")" >&2
        if [[ "$mode" != --repair ]]; then
            failed=1
            continue
        fi
        # No --local: it would omit borrowed objects. Keep the pointer intact
        # until the repack has completed, and restore it if validation fails.
        git --git-dir="$store" repack -a -d
        mv "$alternate" "$alternate.detach-backup"
        if ! git --git-dir="$store" fsck --full --no-reflogs --no-dangling; then
            mv "$alternate.detach-backup" "$alternate"
            echo "fsck failed; restored alternate for $path" >&2
            failed=1
            continue
        fi
        rm "$alternate.detach-backup"
    fi
    if ! git --git-dir="$store" fsck --full --no-reflogs --no-dangling; then
        echo "fsck failed: $path" >&2
        failed=1
    fi
done < <(git config --file "$root/.gitmodules" --get-regexp '^submodule\..*\.path$' | awk '$2 ~ /^player\/vendor\// {print $2}')
exit "$failed"

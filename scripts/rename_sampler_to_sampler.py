#!/usr/bin/env python3
"""
Rename `sampler` -> `sampler` across the repo (case-sensitive, three variants):

    Sampler  -> Sampler
    sampler  -> sampler
    SAMPLER  -> SAMPLER

Scope:
    - File contents (text files only)
    - File names
    - Directory names

Defaults to DRY RUN. Pass --apply to actually perform the changes.

Usage:
    python3 scripts/rename_sampler_to_sampler.py                 # dry run
    python3 scripts/rename_sampler_to_sampler.py --apply         # execute
    python3 scripts/rename_sampler_to_sampler.py --root <path>   # override root
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

# Order matters: longer/more-specific replacements first is not required here
# because the three tokens are disjoint under case-sensitive matching, but we
# keep a stable order for deterministic output.
REPLACEMENTS: list[tuple[str, str]] = [
    ("SAMPLER", "SAMPLER"),
    ("Sampler", "Sampler"),
    ("sampler", "sampler"),
]

# Directories we must never touch.
EXCLUDED_DIRS: set[str] = {
    ".git",
    "node_modules",
    "dist",
    "build",
    ".build",
    ".next",
    ".venv",
    "venv",
    "__pycache__",
    ".idea",
    ".vscode",
    "DerivedData",
    ".xcodeproj",
    ".xcworkspace",
}

# File extensions we treat as binary and skip for content rewriting.
BINARY_EXTS: set[str] = {
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".icns",
    ".pdf", ".zip", ".gz", ".tgz", ".bz2", ".xz", ".7z",
    ".mp3", ".mp4", ".mov", ".wav", ".m4a",
    ".so", ".dylib", ".a", ".o", ".class", ".jar",
    ".exe", ".dll", ".bin", ".wasm",
    ".ttf", ".otf", ".woff", ".woff2", ".eot",
    ".db", ".sqlite", ".sqlite3",
    ".pen",  # encrypted — per MCP notes
    ".DS_Store",
}

# Filenames (basename) to always skip for content rewriting.
SKIP_FILES: set[str] = {
    ".DS_Store",
}


def replace_all(text: str) -> str:
    for old, new in REPLACEMENTS:
        text = text.replace(old, new)
    return text


def should_skip_dir(name: str) -> bool:
    return name in EXCLUDED_DIRS


def is_probably_binary(path: Path) -> bool:
    if path.name in SKIP_FILES:
        return True
    if path.suffix.lower() in BINARY_EXTS:
        return True
    # Heuristic: sniff first 4KB for NUL byte.
    try:
        with path.open("rb") as f:
            chunk = f.read(4096)
        if b"\x00" in chunk:
            return True
    except OSError:
        return True
    return False


def rewrite_file_contents(path: Path, apply: bool) -> bool:
    """Return True if the file content would change / did change."""
    try:
        original = path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return False
    except OSError as e:
        print(f"  ! could not read {path}: {e}", file=sys.stderr)
        return False

    updated = replace_all(original)
    if updated == original:
        return False

    if apply:
        try:
            path.write_text(updated, encoding="utf-8")
        except OSError as e:
            print(f"  ! could not write {path}: {e}", file=sys.stderr)
            return False
    return True


def collect_paths(root: Path) -> tuple[list[Path], list[Path]]:
    """Walk the tree once, returning (files, dirs) as absolute paths.

    Dirs are returned in deepest-first order so renames don't invalidate
    parent paths still in the queue.
    """
    files: list[Path] = []
    dirs: list[Path] = []
    for dirpath, dirnames, filenames in os.walk(root, topdown=True):
        # Prune excluded dirs in place.
        dirnames[:] = [d for d in dirnames if not should_skip_dir(d)]
        for fn in filenames:
            files.append(Path(dirpath) / fn)
        for dn in dirnames:
            dirs.append(Path(dirpath) / dn)
    # Deepest first for dirs so parent renames don't break child paths.
    dirs.sort(key=lambda p: len(p.parts), reverse=True)
    return files, dirs


def rename_path(path: Path, apply: bool) -> Path | None:
    """Rename a single file or directory if its basename contains a token.

    Returns the new Path if a rename would occur (or did occur), else None.
    """
    new_name = replace_all(path.name)
    if new_name == path.name:
        return None
    new_path = path.with_name(new_name)
    if new_path.exists():
        print(f"  ! skip rename (target exists): {path} -> {new_path}", file=sys.stderr)
        return None
    if apply:
        try:
            path.rename(new_path)
        except OSError as e:
            print(f"  ! could not rename {path} -> {new_path}: {e}", file=sys.stderr)
            return None
    return new_path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--apply", action="store_true", help="Actually perform changes (default is dry run).")
    parser.add_argument("--root", default=str(Path(__file__).resolve().parent.parent),
                        help="Repo root (default: parent of this script's dir).")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    if not root.is_dir():
        print(f"Root is not a directory: {root}", file=sys.stderr)
        return 2

    mode = "APPLY" if args.apply else "DRY RUN"
    print(f"[{mode}] root: {root}")
    print(f"[{mode}] replacements: {REPLACEMENTS}")
    print()

    files, dirs = collect_paths(root)

    # 1. Rewrite file contents.
    print("== Content changes ==")
    content_changed = 0
    for f in files:
        if is_probably_binary(f):
            continue
        if rewrite_file_contents(f, apply=args.apply):
            print(f"  ~ {f.relative_to(root)}")
            content_changed += 1
    print(f"  total: {content_changed} file(s)")
    print()

    # 2. Rename files.
    print("== File renames ==")
    file_renames = 0
    for f in files:
        new = rename_path(f, apply=args.apply)
        if new is not None:
            print(f"  -> {f.relative_to(root)}  =>  {new.relative_to(root)}")
            file_renames += 1
    print(f"  total: {file_renames} file(s)")
    print()

    # 3. Rename directories (deepest first).
    print("== Directory renames ==")
    dir_renames = 0
    for d in dirs:
        # In dry-run the directory still exists at its original path.
        # In apply mode, a parent dir rename earlier in the list would not
        # happen because we sorted deepest-first — children rename before
        # parents, so `d` is still valid when we reach it.
        if not d.exists():
            # A parent may have been renamed (shouldn't happen with the sort,
            # but guard anyway). Recompute under current layout.
            continue
        new = rename_path(d, apply=args.apply)
        if new is not None:
            print(f"  -> {d.relative_to(root)}  =>  {new.relative_to(root)}")
            dir_renames += 1
    print(f"  total: {dir_renames} dir(s)")
    print()

    print(f"[{mode}] done. content={content_changed} files_renamed={file_renames} dirs_renamed={dir_renames}")
    if not args.apply:
        print("Re-run with --apply to perform these changes.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

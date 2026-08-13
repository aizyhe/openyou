#!/usr/bin/env python3
"""
Package userdata/ into the v3 backup zip shape /api/backup/import accepts
(and the app's strict filename check expects).
The manifest carries ONLY version + timestamp + sha256 — no DB sections, since
the PC has no phone DB and import treats an absent section as "leave those rows
alone"; file restore is additive, so the phone gains what it lacks untouched.
"""
import hashlib
import json
import os
import sys
import time
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "userdata"
# The identity core the daemon's strict import validation requires.
REQUIRED = ["inside", "rule", "skill"]
# Only these top-level dirs are valid in a v3 backup — import rejects anything else, so skip them.
CONCEPT_DIRS = ["inside", "rule", "skill", "star", "task", "calendar", "memory", "consult"]


def main():
    if not SRC.is_dir():
        sys.exit(f"error: {SRC} not found")
    for req in REQUIRED:
        if not (SRC / req).is_dir():
            sys.exit(f"error: userdata/{req}/ missing — refusing to build an incomplete backup")

    stamp = time.strftime("%Y%m%d-%H%M%S")
    out = ROOT / f"openyou-backup-{stamp}.zip"

    # sha256(rel + \x00 + content) sorted by rel — must match the daemon's export or import rejects.
    files = []
    skipped = []
    for dirpath, dirnames, filenames in os.walk(SRC):
        # Mirror the daemon export's visibility rules: skip dot-dirs and symlinks.
        dirnames[:] = [d for d in dirnames
                       if not d.startswith(".") and not os.path.islink(os.path.join(dirpath, d))]
        for fn in sorted(filenames):
            if fn.startswith("."):
                continue
            full = Path(dirpath) / fn
            if full.is_symlink() or not full.is_file():
                continue
            rel = full.relative_to(SRC).as_posix()
            if rel == "manifest.json":
                continue  # reserved for the manifest written below
            top = rel.split("/", 1)[0]
            if top not in CONCEPT_DIRS:
                skipped.append(rel)  # import would reject it — don't ship it
                continue
            files.append((rel, full))
    files.sort(key=lambda rf: rf[0])
    for rel in skipped:
        print(f"   (skipped non-backup path: {rel})")

    h = hashlib.sha256()
    for rel, full in files:
        # os.fsencode matches Go's []byte(rel) byte semantics and survives non-UTF-8 filenames (surrogates).
        h.update(os.fsencode(rel))
        h.update(b"\x00")
        h.update(full.read_bytes())

    n = 0
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        # Identity-core dirs always ship even empty — import requires inside/rule/skill to exist.
        for core in REQUIRED:
            z.writestr(f"userdata/{core}/", "")
        for rel, full in files:
            z.write(full, "userdata/" + rel)
            n += 1
        manifest = {"version": "3.0", "timestamp": time.time_ns(), "sha256": h.hexdigest()}
        z.writestr("userdata/manifest.json", json.dumps(manifest, indent=2))

    print(f"✅ {out.name}  ({n} files)")
    print(f"   Import on the phone: Settings → Backup → Import (keep the file name unchanged).")


if __name__ == "__main__":
    main()

"""Render release installation guidance from the actual archive's build metadata."""

import argparse
import json
from pathlib import Path
from urllib.parse import quote
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    with zipfile.ZipFile(args.archive) as archive:
        metadata = json.loads(archive.read("BUILD-INFO.json"))
    if metadata["os"] != "windows" or metadata["arch"] != "amd64" or metadata["cgo"]:
        raise ValueError("release guidance requires a standalone Windows x64 archive")
    template = Path(__file__).resolve().parent.parent / "packaging/windows/release-notes.md"
    notes = template.read_text(encoding="utf-8")
    for key, value in {
        "TAG": metadata["version"], "COMMIT": metadata["commit"], "GO_VERSION": metadata["go"],
        "RELEASE_URL": f"https://github.com/{args.repo}/releases/download/{quote(metadata['version'], safe='')}",
    }.items():
        notes = notes.replace(f"@{key}@", value)
    args.output.write_text(notes, encoding="utf-8")


if __name__ == "__main__":
    main()

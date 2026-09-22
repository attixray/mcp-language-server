"""Build and archive one standalone bridge binary (no Go runtime required)."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", choices=["windows", "linux", "darwin"], required=True)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", type=Path, default=Path("dist"))
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", args.version):
        parser.error("version must be a safe archive filename component")

    root = Path(__file__).resolve().parent.parent
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS=args.os, GOARCH=args.arch, CGO_ENABLED="0")
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    go_version = subprocess.check_output(["go", "version"], cwd=root, text=True).strip()
    name = f"mcp-language-server_{args.version}_{args.os}_{args.arch}"
    with tempfile.TemporaryDirectory(prefix="bridge-release-") as temporary:
        staging = Path(temporary)
        binary = staging / ("mcp-language-server.exe" if args.os == "windows" else "mcp-language-server")
        subprocess.run(
            ["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(binary), "."],
            cwd=root, env=env, check=True,
        )
        binary.chmod(0o755)
        metadata = staging / "BUILD-INFO.json"
        metadata.write_text(json.dumps({
            "version": args.version, "commit": commit, "go": go_version,
            "os": args.os, "arch": args.arch, "cgo": False,
        }, indent=2) + "\n", encoding="utf-8")
        files = [binary, root / "LICENSE", root / "README.md", metadata]
        if args.os == "windows":
            archive = output / f"{name}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as package:
                for file in files:
                    package.write(file, file.name)
        else:
            archive = output / f"{name}.tar.gz"
            with tarfile.open(archive, "w:gz") as package:
                for file in files:
                    package.add(file, arcname=file.name)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_name(archive.name + ".sha256").write_text(f"{digest}  {archive.name}\n", encoding="ascii")
    print(archive)


if __name__ == "__main__":
    main()

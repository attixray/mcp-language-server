"""Wrap a verified Windows x64 release ZIP in a per-user Inno Setup installer."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import uuid
import zipfile

FILES = {"mcp-language-server.exe", "LICENSE", "README.md", "BUILD-INFO.json",
         "DesignTimeIsolation.targets"}


def package(archive, compiler, output, app_id=None):
    archive = archive.resolve()
    checksum = archive.with_name(archive.name + ".sha256").read_text(encoding="ascii").split()
    if (len(checksum) != 2 or checksum[1] != archive.name or
            hashlib.sha256(archive.read_bytes()).hexdigest() != checksum[0]):
        raise ValueError("release archive checksum mismatch")
    with zipfile.ZipFile(archive) as source:
        if len(source.namelist()) != len(FILES) or set(source.namelist()) != FILES:
            raise ValueError("unexpected release archive contents")
        metadata = json.loads(source.read("BUILD-INFO.json"))
        if metadata["os"] != "windows" or metadata["arch"] != "amd64":
            raise ValueError("installer requires a Windows amd64 release")
        version = metadata["version"]
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", version):
            raise ValueError("unsafe release version")
        match = re.fullmatch(r"v?(\d+)\.(\d+)\.(\d+)(?:-[A-Za-z0-9._-]+)?", version)
        numbers = [int(number) for number in match.groups()] if match else [0, 0, 0]
        if any(number > 65535 for number in numbers):
            raise ValueError("version components exceed Windows version limits")
        numeric = ".".join(str(number) for number in numbers) + ".0"
        output = output.resolve()
        output.mkdir(parents=True, exist_ok=True)
        name = f"mcp-language-server_{version}_windows_amd64_setup"
        root = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory(prefix="bridge-installer-") as temporary:
            payload = Path(temporary)
            # Exact name allowlist above excludes directories and traversal.
            source.extractall(payload)
            defines = []
            if app_id is not None:
                # Isolated uninstall registration for the installer smoke test.
                defines.append(f"/DAppGuid={uuid.UUID(app_id)}")
            subprocess.run([
                str(compiler), f"/DPayloadDir={payload}", f"/DReleaseVersion={version}",
                f"/DNumericVersion={numeric}", f"/DInstallerName={name}",
                f"/O{output}", str(root / "packaging/windows/installer.iss"),
            ] + defines, check=True)
    installer = output / (name + ".exe")
    digest = hashlib.sha256(installer.read_bytes()).hexdigest()
    installer.with_name(installer.name + ".sha256").write_text(
        f"{digest}  {installer.name}\n", encoding="ascii")
    return installer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--iscc", type=Path, required=True)
    parser.add_argument("--output", type=Path, default=Path("dist"))
    args = parser.parse_args()
    print(package(args.archive, args.iscc.resolve(), args.output))


if __name__ == "__main__":
    main()

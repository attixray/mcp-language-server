"""Exercise the real installer using an isolated AppId and temporary directory."""

import argparse
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time
import uuid
import winreg
import zipfile

from package_windows_installer import FILES, package


def run_setup(executable, log, directory=None, success=True):
    command = [str(executable), "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART",
               "/SP-", f"/LOG={log}"]
    if directory is not None:
        command.append(f"/DIR={directory}")
    result = subprocess.run(command, timeout=90, creationflags=subprocess.CREATE_NO_WINDOW)
    if success and result.returncode != 0:
        raise AssertionError(f"Setup failed ({result.returncode}): {log.read_text(errors='replace')}")
    if not success and result.returncode == 0:
        raise AssertionError("Setup unexpectedly succeeded while the executable was in use")
    return result.returncode


def verify_files(directory, archive):
    with zipfile.ZipFile(archive) as source:
        for name in FILES:
            assert (directory / name).read_bytes() == source.read(name), name


def wait_for_uninstaller_cleanup(directory):
    # Inno's temporary helper deletes the running uninstaller after it exits.
    deadline = time.monotonic() + 15
    while list(directory.glob("unins*.exe")) and time.monotonic() < deadline:
        time.sleep(0.1)
    assert not list(directory.glob("unins*.exe")), "uninstaller helper did not finish"


class MCPProcess:
    def __init__(self, binary, workspace, version):
        helper = workspace / "fake_lsp.py"
        helper.write_text('''import json, sys
while True:
    headers = {}
    while True:
        line = sys.stdin.buffer.readline()
        if not line: sys.exit(0)
        if line in (b'\\n', b'\\r\\n'): break
        key, value = line.decode().split(':', 1)
        headers[key.lower()] = value.strip()
    request = json.loads(sys.stdin.buffer.read(int(headers['content-length'])))
    if request.get('method') == 'exit': break
    if 'id' in request:
        result = {'capabilities': {}} if request.get('method') == 'initialize' else None
        body = json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}).encode()
        sys.stdout.buffer.write(('Content-Length: %d\\r\\n\\r\\n' % len(body)).encode() + body)
        sys.stdout.buffer.flush()
''', encoding="utf-8")
        env = dict(os.environ, PATH="", LOG_LEVEL="ERROR")
        self.process = subprocess.Popen([
            str(binary), "--shared=false", "--workspace", str(workspace),
            "--lsp", sys.executable, "--", str(helper),
        ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", env=env, creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            received = queue.Queue()
            threading.Thread(target=lambda: received.put(self.process.stdout.readline()), daemon=True).start()
            request = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                "protocolVersion": "2024-11-05", "capabilities": {},
                "clientInfo": {"name": "installer-smoke", "version": "1"}}}
            self.process.stdin.write(json.dumps(request) + "\n")
            self.process.stdin.flush()
            response = json.loads(received.get(timeout=20))
            assert response["result"]["serverInfo"]["version"] == version, response
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.process.stdin and not self.process.stdin.closed:
            self.process.stdin.close()
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=10)
        for stream in (self.process.stdout, self.process.stderr):
            stream.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--iscc", type=Path, required=True)
    args = parser.parse_args()
    archive, compiler = args.archive.resolve(), args.iscc.resolve()
    with zipfile.ZipFile(archive) as source:
        release_version = json.loads(source.read("BUILD-INFO.json"))["version"]
        version = release_version.removeprefix("v")
    app_id = str(uuid.uuid4())
    key_name = rf"Software\Microsoft\Windows\CurrentVersion\Uninstall\{{{app_id}}}_is1"
    registry_view = winreg.KEY_READ | winreg.KEY_WOW64_64KEY
    root = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="mcp-installer-smoke-") as temporary:
        scratch = Path(temporary)
        directory = scratch / "custom install path"
        old_packages = scratch / "old-packages"
        subprocess.run([sys.executable, str(root / "scripts/package_release.py"),
                        "--os", "windows", "--arch", "amd64", "--version", "v0.0.0",
                        "--output", str(old_packages)], check=True)
        old_archive = next(old_packages.glob("*.zip"))
        old_setup = package(old_archive, compiler, scratch / "old-setup", app_id)
        new_setup = package(archive, compiler, scratch / "new-setup", app_id)
        active = None
        try:
            run_setup(old_setup, scratch / "clean.log", directory)
            verify_files(directory, old_archive)
            marker = directory / "user-settings.txt"
            marker.write_text("preserve me", encoding="utf-8")
            active = MCPProcess(directory / "mcp-language-server.exe", scratch, "0.0.0")
            before = {name: (directory / name).read_bytes() for name in FILES}
            run_setup(new_setup, scratch / "locked.log", success=False)
            assert "Stop its MCP" in (scratch / "locked.log").read_text(errors="replace")
            assert active.process.poll() is None, "Setup terminated the running MCP"
            assert before == {name: (directory / name).read_bytes() for name in FILES}
            run_setup(directory / "unins000.exe", scratch / "locked-uninstall.log", success=False)
            assert active.process.poll() is None
            assert before == {name: (directory / name).read_bytes() for name in FILES}
            active.close()
            active = None
            # No /DIR: reuse the registered custom directory from the old install.
            run_setup(new_setup, scratch / "upgrade.log")
            verify_files(directory, archive)
            with winreg.OpenKey(winreg.HKEY_CURRENT_USER, key_name, 0, registry_view) as key:
                assert Path(winreg.QueryValueEx(key, "InstallLocation")[0]) == directory
                assert winreg.QueryValueEx(key, "DisplayVersion")[0] == release_version
            assert marker.read_text(encoding="utf-8") == "preserve me"
            # Repair must restore a missing payload file and keep the same path.
            (directory / "README.md").unlink()
            run_setup(new_setup, scratch / "repair.log")
            verify_files(directory, archive)
            active = MCPProcess(directory / "mcp-language-server.exe", scratch, version)
            active.close()
            active = None
            run_setup(directory / "unins000.exe", scratch / "uninstall.log")
            wait_for_uninstaller_cleanup(directory)
            assert not any((directory / name).exists() for name in FILES)
            assert marker.read_text(encoding="utf-8") == "preserve me"
            assert not registry_exists(key_name, registry_view)
            # An unregistered ZIP install can be explicitly selected as /DIR.
            with zipfile.ZipFile(old_archive) as source:
                source.extractall(directory)
            run_setup(new_setup, scratch / "portable.log", directory)
            verify_files(directory, archive)
            run_setup(directory / "unins000.exe", scratch / "portable-uninstall.log")
            wait_for_uninstaller_cleanup(directory)
            assert not registry_exists(key_name, registry_view)
        finally:
            if active is not None:
                active.close()
            uninstaller = directory / "unins000.exe"
            if uninstaller.exists():
                run_setup(uninstaller, scratch / "cleanup.log")
                wait_for_uninstaller_cleanup(directory)
    print("PASS: clean install, locked upgrade/uninstall, preserved path/settings, upgrade, repair, MCP version, uninstall, ZIP migration")


def registry_exists(name, view):
    try:
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, name, 0, view):
            return True
    except FileNotFoundError:
        return False


if __name__ == "__main__":
    main()

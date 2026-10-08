# Fork releases

Pushing a new `v*` tag runs the **Release binaries** workflow. It tests core
navigation on Windows, Linux and macOS, then builds a Windows x64 (`amd64`)
ZIP archive. A per-user Windows x64 `.exe` installer is also built from the
verified ZIP, using
the pinned Inno Setup compiler. It requires no Go runtime or administrator rights.

Each archive contains the binary, license, README, BUILD-INFO.json with the
source commit and compiler version, and DesignTimeIsolation.targets (see
[docs/bari-coexistence.md](docs/bari-coexistence.md)). SHA256SUMS covers the ZIP
and the installer. These are the only two release packages. The installer job tests clean installation, locked upgrade
and uninstall rejection, directory/settings preservation, upgrade, repair,
MCP version reporting, uninstall and migration from an extracted ZIP. The
workflow verifies checksums, uploads to a draft release, and publishes only
after all uploads succeed. Tags containing a hyphen produce prereleases.
The release description combines generated change notes with installation and
upgrade guidance rendered from `packaging/windows/release-notes.md` and the
ZIP's actual build metadata, so download links and source information match the tag.

Use `v<major>.<minor>.<patch>` for a stable release, such as `v1.0.0`.
Hyphenated tags such as `v1.0.1-rc.1` or the older `v0.1.1-attixray.3`
convention are prereleases.

First update the default `Version` in `internal/version/version.go` (without
the leading `v`) and commit it. MCP server information and LSP client information
use this same version. Release packaging also sets it from the tag at link time,
so the executable and `BUILD-INFO.json` identify the same release.
From the intended, clean release commit, for example:

```sh
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

Create a new tag for each release; do not move published tags. If an upload
fails after draft creation, inspect the draft before retrying; the workflow
intentionally does not replace existing release assets.

**Run workflow** in GitHub Actions (workflow_dispatch) performs the same tests
and packaging but creates no release. Download those test packages from the
workflow's artifacts. The full CI workflow covers external-language integration
tests and a Linux race-detector run. The release gate covers all internal tests,
including filesystem watchers and stdio protocol tests on all three platforms.

No extra secrets are needed: only the publish job gets `contents: write`.
The Go compiler version is pinned in the workflow. Update it deliberately.
The downloaded binary does not require Go; install the desired LSP server
(for example csharp-ls) separately.

To build one package locally with Go, Git and Python 3 available:

```sh
python scripts/package_release.py --os windows --arch amd64 --version snapshot-local
```

To build the Windows x64 installer locally on Windows (PowerShell):

```powershell
$compiler = ./scripts/ensure_inno_setup.ps1 -Destination ./dist/inno-setup
python scripts/package_windows_installer.py --archive ./dist/mcp-language-server_snapshot-local_windows_amd64.zip --iscc "$compiler"
python scripts/test_windows_installer.py --archive ./dist/mcp-language-server_snapshot-local_windows_amd64.zip --iscc "$compiler"
```

The packaging script verifies the ZIP's adjacent `.sha256` file, its target
architecture and its exact file list before compiling. It writes the installer
and an adjacent `.sha256` file. The compiler bootstrap verifies a pinned SHA-256
before running the downloaded Inno Setup installer. Go is needed by the smoke
test to build an older fixture, but not by installer packaging from an existing ZIP.

Keep `AppGuid` in `packaging/windows/installer.iss` unchanged across releases.
It identifies the existing installation, preserves its directory and reuses
its uninstall entry. The tests use a random AppGuid and temporary directory
so they do not interfere with a developer's real installation. The installer
does not modify MCP configurations, PATH, language servers or broker logs/cache.
Downloads are currently unsigned; no code-signing credentials are configured.

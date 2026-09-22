# Fork releases

Pushing a new `v*` tag runs the **Release binaries** workflow. It tests core
navigation on Windows, Linux and macOS, then builds six standalone binaries:
Windows/Linux/macOS (`darwin`), each for `amd64` and `arm64`. Windows downloads
are ZIP files; Linux and macOS downloads are tar.gz files.

Each archive contains the binary, license, README and BUILD-INFO.json with the
source commit and compiler version. SHA256SUMS covers all six archives. The
workflow verifies checksums, uploads to a draft release, and publishes only
after all uploads succeed. Tags containing a hyphen produce prereleases.

Use fork-specific tags, for example `v0.1.1-attixray.1`, to distinguish these
builds from upstream releases. From the intended, clean release commit:

```sh
git tag -a v0.1.1-attixray.1 -m "First attixray prerelease"
git push origin v0.1.1-attixray.1
```

Create a new tag for each release; do not move published tags. If an upload
fails after draft creation, inspect the draft before retrying; the workflow
intentionally does not replace existing release assets.

**Run workflow** in GitHub Actions (workflow_dispatch) performs the same tests
and packaging but creates no release. Download those test packages from the
workflow's artifacts. The full CI workflow separately covers watcher and
external-language integration tests; the release gate covers core unit and
stdio protocol tests without requiring extra language-server installations.

No extra secrets are needed: only the publish job gets `contents: write`.
The Go compiler version is pinned in the workflow. Update it deliberately.
The downloaded binary does not require Go; install the desired LSP server
(for example csharp-ls) separately. macOS binaries are unsigned/not notarized.

To build one package locally with Go, Git and Python 3 available:

```sh
python scripts/package_release.py --os windows --arch amd64 --version snapshot-local
```

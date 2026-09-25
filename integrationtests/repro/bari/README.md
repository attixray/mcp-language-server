# bari coexistence reproduction

This reproduces bari builds failing while csharp-ls sessions are loaded on the
same working copy. The design note [docs/bari-coexistence.md](../../../docs/bari-coexistence.md)
explains the findings. The reproduction needs Windows, the .NET SDK with the
WPF targets, csharp-ls and PowerShell 7. The tests skip unless `BARI_REPRO=1`
is set.

- `bari.ps1` mimics bari's generated layout: projects inside the source tree,
  unconditional intermediate paths under `target/tmp`, solutions under
  `target/`, and a clean that deletes both. Its commands are `generate`,
  `clean`, `build`, `rebuild` and `designtime`. The last one runs Roslyn-style
  design-time builds in a loop.
- `TestBariCoexistence` starts `REPRO_SESSIONS` bridge sessions (default 2)
  with csharp-ls. It runs clean, build and rebuild `REPRO_CYCLES` times
  (default 3). It then checks that definition and references are still
  correct, and that each session's processes exit when its client closes
  stdin or kills the bridge.
- `TestDesignTimeRace` runs no language server. It races the design-time loop
  against builds.

| variable | meaning |
|---|---|
| `BRIDGE_BIN` | the bridge binary under test |
| `CSHARP_LS` | csharp-ls executable (default: from `PATH`) |
| `REPRO_SESSION_ENV` | extra environment for the language servers or the design-time loop, `;;`-separated, e.g. `CustomBeforeMicrosoftCommonTargets=C:\...\DesignTimeIsolation.targets` |
| `REPRO_OUTPUT` | output directory for the workspace, logs and `summary.md` |
| `REPRO_MODULES` | number of WPF modules (default 6) |

```powershell
go build -o $env:TEMP\bridge.exe .
$env:BARI_REPRO = '1'; $env:BRIDGE_BIN = "$env:TEMP\bridge.exe"
go test -v -count=1 -timeout 60m -run '^TestBariCoexistence$' ./integrationtests/repro/bari/
```

The workflow `.github/workflows/bari-coexistence.yml` runs every variant on
`windows-latest`: the race with and without isolation, a control without
language servers, and the v0.1.1-attixray.2 bridge and this branch, each with
and without isolation.

# bari builds and C# language servers on one working copy

Design note for running `bari build|clean|rebuild` on the QVI EVOLVE Suite
while `csharp-ls` sessions are loaded on the same tree: from this bridge
(Codex) and from the `csharp-lsp` plugin (Claude Code), several at once.

## Summary

- **H1 is the cause of CS2001 and BG1002.** A csharp-ls design-time build runs
  the WPF markup compiler in real-build mode. It writes into the intermediate
  directory bari's build uses, and it deletes the build's generated files.
- **H2 is why this happened during builds.** Every regenerated `.csproj` and
  every temporary WPF project made csharp-ls reload the whole solution, 20–54
  times per build per session.
- **The fix has two parts, and neither changes bari or the Suite:**
  - The bridge no longer forwards build churn (0 reloads per build in the
    reproduction).
  - `DesignTimeIsolation.targets`, set through the language server's
    environment, moves design-time builds into their own directory. This also
    covers the Claude Code plugin.
- **H3 is real on two paths.**
  - Design-time markup compilation locks referenced build outputs (MSB3026,
    "Failed to clean target root"). The targets file now hands it copies:
    0 locks in 52 207 probes, against 226 without it.
  - bari-vs-addon's solution watcher hashes project files in devenv.exe
    without `FILE_SHARE_DELETE`, which can fail `CsprojCleaner`. It is fixed
    in add-on 1.12.3 (attixray/bari-vs-addon#1).
- **H4:** csharp-ls exits when its bridge dies. The bridge now also exits when
  its parent does. A job object takes the language server's process tree down
  with the bridge.
- **bari and the add-on were fixed at the source afterwards.**
  - bari 1.1.0 (`attixray/bari`, .NET 10) writes identical project files
    across cleans, retries blocked deletes and names the holder, and keeps
    `target/.vs`.
  - Add-on 1.12.6 loads in the background and cancels bari's whole process
    tree at once.
  - The owner verified both on the Suite on 2026-09-25; see
    [Local verification](#local-verification-2026-09-25).

## Evidence

The reproduction is under `integrationtests/repro/bari` and runs with
`.github/workflows/bari-coexistence.yml` on `windows-latest`. It uses
.NET SDK 10.0.401 and csharp-ls 0.28.0, the installed version.

`bari.ps1` generates projects the way bari does:

- SDK-style projects inside the source tree;
- unconditional `BaseIntermediateOutputPath` and `IntermediateOutputPath`
  under `target/tmp/<Module>/<Project>`;
- configuration and platform `Bari`;
- WPF pages that reference local types, so the markup compiler creates
  `*_wpftmp.csproj`;
- solutions under `target/`;
- a clean that deletes `target/` and then every project file with a plain
  `File.Delete`.

It has two tests:

- `TestBariCoexistence` loads two csharp-ls sessions through the bridge and
  runs clean, build and rebuild three times each.
- `TestDesignTimeRace` runs design-time builds back to back, with the global
  properties and targets of Roslyn's .NET build host, while bari builds.

## Findings

### H1: shared intermediate directory — confirmed

#### How csharp-ls reaches the WPF markup compiler

- csharp-ls opens the solution with Roslyn's `MSBuildWorkspace`.
- Roslyn's build host builds `Compile`, `CoreCompile` and
  `DesignTimeMarkupCompilation` (`ProjectBuildManager.cs`).
- It sets these global properties: `DesignTimeBuild=true`,
  `BuildingProject=false`, `SkipCompilerExecution=true`,
  `ProvideCommandLineArgs=true` and `BuildProjectReferences=false`.
- csharp-ls adds `TargetFramework`. It passes no other property.
- `Microsoft.WinFX.targets` adds `DesignTimeMarkupCompilation` to
  `CoreCompileDependsOn` when `DesignTimeBuild` is true. That target calls
  `MarkupCompilePass1`.
- The generated project sets `IntermediateOutputPath` unconditionally, so the
  design-time pass writes to the same `target/tmp/<Module>/<Project>/` as
  bari's build.

#### Why the pass behaves like a real build

The markup compiler writes IntelliSense files (`*.g.i.cs`, separate
`*.i.cache` state) only when Visual Studio provides a host file manager
(`TaskFileService.IsRealBuild`). csharp-ls provides none. Its design-time pass
is therefore a real pass 1:

- it writes `*.g.cs`, `GeneratedInternalTypeHelper.g.cs`,
  `<Assembly>_MarkupCompile.cache` and `.lref`;
- when its settings differ from the previous pass it recompiles every page.
  csharp-ls evaluates Debug while bari builds Bari, so `DefineConstants`
  differ every time, and every pass differs.

`CleanupGeneratedFiles` runs first and deletes each page's `.g.cs` and
`.baml`. Pass 2 regenerates the BAML of pages with local types, and a
design-time build never runs pass 2. Interleaved with bari's own
pass 1 → temporary assembly → pass 2 → resources, this produces:

- **CS2001:** a `.g.cs` is missing while `*_wpftmp.csproj` compiles;
- **BG1002:** a `.baml` is missing when resources are generated.

#### Observed in CI

In run 36103437838 ("bridge under test", no isolation), two sessions loaded
after the initial build. In `target/tmp/Mod1/Extensions.Mod1`, the load:

- rewrote `_MarkupCompile.cache` (795 → 801 bytes);
- rewrote `GeneratedInternalTypeHelper.g.cs` (7 → 2960 bytes);
- rewrote every `Panel*.g.cs` (4461 → 4470 bytes);
- **deleted every `*.baml`**.

With `DesignTimeIsolation.targets` ("baseline bridge, design-time isolation"),
the same load wrote only to `…/Extensions.Mod1/designtime/`. The build's own
files kept their build timestamps.

No `*.g.i.cs` appeared in any run. The 641 `*.g.i.cs` files on the owner's
machine therefore cannot have come from csharp-ls. They need the Visual Studio
host, and the owner confirms Visual Studio was most likely open on the Suite
at the time. Visual Studio's own design-time builds write IntelliSense files
next to the build's; they do not run pass 1 in real-build mode.

### H2: build-triggered reload storm — confirmed

csharp-ls 0.28.0 registers `**/*.{cs,cshtml,csproj,sln,slnx}`. Any `.csproj`,
`.sln` or `.slnx` event requests a solution reload after a 5 s sliding quiet
period (`Handlers/Workspace.fs`, `Runtime/ServerStateLoop.fs`). A reload is a
full design-time build of every project (H1).

The bridge forwarded:

- bari's deletion and regeneration of every project file;
- the markup compiler's `*_wpftmp.csproj` creation and deletion. These come
  and go throughout the build, so reloads kept restarting while it ran.

The bridge excludes `target/`, so the `.sln` rewrites never reached csharp-ls.

| run | bridge | reload notices per step and session |
|---|---|---|
| 36102705317 | v0.1.1-attixray.2 | 20–30 |
| 36103437838 | v0.1.1-attixray.2 | 30–54 |
| 36103437838 | this branch | **0** |

With this branch, both sessions answered definition and references queries
correctly right after the last build. bari regenerates the same content, so
the loaded solution stays valid.

**Project GUIDs change on every clean.** The installed `bari.exe` 1.0.3.68 was
built on 2026-09-10 at 08:29:53, 79 s after `p5ych08illy/bari` commit
`9386ad0`, so it is that revision. It includes `86f4724` (absolute
`BaseIntermediateOutputPath`), which the reproduction mirrors. At that
revision `PropertiesSection` writes `<ProjectGuid>` from
`DefaultProjectGuidManagement`, which keeps GUIDs in `cache/<goal>/guids`.
`clean`, and therefore `rebuild`, deletes that cache unless `--soft-clean` is
given, so every project file comes back with a new GUID. Roslyn identifies
projects by path, so the gate ignores `<ProjectGuid>` when comparing content.
`bari.ps1` assigns GUIDs the same way. bari 1.1.0 derives new GUIDs from the
suite and project names, so a clean no longer changes them; the gate still
ignores the element for older bari builds.

### H3: handles without share-delete

**Mechanism.** .NET's `FileStream`, which MSBuild uses to read project files,
opens with `FileShare.Read` and no `FILE_SHARE_DELETE`. So does Go's
`os.Open` on Windows. While any evaluation reads a `.csproj`, bari's
`File.Delete` of that file fails with a sharing violation. An evaluation
happens on every reload, so H2 made reads coincide with `CsprojCleaner`.

This bridge never read project files before this change. It now does, to
compare content, and opens them with `FILE_SHARE_DELETE`.

**Referenced output assemblies are locked too.** The markup compiler keeps
every referenced assembly locked while it runs. In the WPF source this is
`ReflectionHelper`, disposed "to release file locks on assemblies". A
design-time pass therefore locks the build's own outputs under
`target/<Module>/`. In `TestDesignTimeRace` (run 36103756250), design-time
builds racing bari builds caused:

- without isolation: `MSB3026` copy retries on `target/Mod4/Extensions.Mod4.dll`;
- with isolation: `Failed to clean target root: … 'Core.Model.dll' … being used
  by another process`, the warning the owner's `rebuild` printed.

Isolation moves writes, not reads. So `DesignTimeIsolation.targets` also gives
the design-time markup compiler copies of the references that are build
outputs, and restores the real references before `CoreCompile`.

The race probes every output assembly for writing while design-time builds
run alone for 30 s, and asks the Restart Manager who holds a locked one:

| run | variant | locked / attempts | holders |
|---|---|---|---|
| 36104835546 | no isolation | 226 / 48 248 | |
| 36104835546 | copies of direct references only | 127 / 48 840 | |
| 36106161468 | copies of direct references only | 40 / 43 290 | the `dotnet msbuild` design-time build of each project that references the locked output *transitively*, e.g. `Extensions.Mod1.dll` held by the builds of Mod3–Mod6 and Shell |
| 36106714461 | direct and transitive copies | **0 / 48 914** | |
| 36107210717 | direct and transitive copies | **0 / 52 207** | |

Transitive project references come from the assets file with
`NuGetPackageId` set, so the first filter treated them as packages. Project
references are now matched by `ReferenceSourceTarget`. With the copies, no
race step showed MSB3026 or "Failed to clean target root", and the sessions
with isolation still answered definition and references correctly, so the
real references are restored.

**bari-vs-addon holds project files too.** The add-on (1.12.2) watches
`src/` from devenv.exe and, 330 ms after a change, MD5-hashes every
project and source file in the changed directories with
`FileShare.ReadWrite`, without `FileShare.Delete`. A bari clean deleting the
generated `.csproj` files, or the markup compiler deleting its
`*_wpftmp.csproj`, triggers exactly that. Visual Studio was most likely open
when `EvolveHelp.csproj` could not be deleted, so devenv.exe is the most
likely holder there; this was not observed on the owner's machine. See
[Visual Studio builds and bari-vs-addon](#visual-studio-builds-and-bari-vs-addon).

**Not established:** the holder of `EvolveHelp.csproj` on the owner's machine.
Deleting project files while design-time builds read them did not fail in
any run. `bari.ps1` records holders through the Restart Manager whenever a
delete fails.

**Metadata references are not locked.** A loaded csharp-ls holds no lock on
output assemblies between loads. Roslyn's `MetadataService` reads metadata
references with `PEStreamOptions.PrefetchEntireImage`, then closes the file.

### H4: lifecycle

- **csharp-ls exits on stdin EOF.** See `Runtime/JsonRpc.fs`. In every run,
  closing the bridge's stdin or killing the bridge ended csharp-ls within the
  20 s check.
- **So PID 29596 had a live bridge.** An orphaned csharp-ls implies its
  bridge was still running: either the Codex process was still alive, or the
  bridge never saw EOF.
- **The bridge could miss its parent exiting on Windows.** Its parent check
  was POSIX-only (`Getppid() == 1`), and Windows does not reparent orphans.
  A copy of the client's stdin handle inherited by another process keeps EOF
  from arriving.

## Changes in this fork

1. **Project-file gate** (`internal/watcher/projects.go`).
   - Events for `.csproj`, `.fsproj`, `.vbproj`, `.sln`, `.slnx`, `.props` and
     `.targets` are held until the files have been quiet for
     `--project-settle`, 30 s by default.
   - Then only the net content changes are reported, in one notification. A
     file deleted and regenerated with the content csharp-ls loaded is never
     reported. `<ProjectGuid>` is ignored in the comparison.
   - `*_wpftmp.*` projects and `obj/` directories are ignored. Their activity
     extends the wait.
   - Source files are forwarded as before.
2. **`contrib/msbuild/DesignTimeIsolation.targets`, shipped in the release
   archives.**
   - Set `CustomBeforeMicrosoftCommonTargets` to its path in the language
     server's environment.
   - MSBuild imports it at the start of `Microsoft.Common.CurrentVersion.targets`,
     after the generated project body and before anything is derived from
     `IntermediateOutputPath`.
   - For `DesignTimeBuild=true` it appends `designtime\` to the intermediate
     path. Real builds import it and change nothing.
   - For the design-time markup compiler it swaps referenced build outputs,
     direct and transitive, for copies under `designtime\references\`, and
     restores the real references afterwards.
   - The project never sets this property, so an environment variable can
     supply it. By contrast, `IntermediateOutputPath` cannot be overridden
     from the environment, because the project sets it.
3. **Lifecycle on Windows** (`lifecycle_windows.go`).
   - The bridge puts itself in a kill-on-close job object. The language server
     and its descendants inherit the job, so they die with the bridge however
     it exits.
   - The bridge waits on its parent's process handle, and treats a PID reused
     by a younger process as not its parent.

## Visual Studio builds and bari-vs-addon

At the start of this work the installed add-on was 1.12.2
(`attixray/bari-vs-addon` at `d42404b`); `zvrana/bari-vs-addon` holds only the
2014 code. The owner now runs 1.12.6.

- **Solution and selection commands.** The add-on intercepts Build, Rebuild
  and Clean Solution, Build and Rebuild Selection, Start Without Debugging,
  and Start when a build is needed. It runs
  `bari --target <goal> <action> <product>` in the Suite root. These are bari
  builds and are covered like command-line ones. Cancel kills bari's whole
  process tree.
- **Project context-menu builds.** Build, Rebuild and Clean from a project's
  context menu (`BuildCtx`, `RebuildCtx`, `CleanCtx`, `CleanSel`), and C++
  projects built directly, bypass the add-on. MSBuild builds into the same
  `target/tmp` directories and does not regenerate project files:
  - the bridge ignores the `*_wpftmp.csproj` churn;
  - with the variable set, design-time builds write elsewhere;
  - the reference copies keep design-time builds from locking the outputs
    Visual Studio replaces (MSB3026 for C#, LNK1104 for C++/CLI links).
- **C++.** csharp-ls never builds `.vcxproj` files. It only resolves C++/CLI
  outputs referenced by C# projects, and those references get the same copies.
- **Visual Studio's own design-time builds** are excluded by default
  (`BuildingInsideVisualStudio`): its fast up-to-date check reads intermediate
  paths from them. `DesignTimeIsolationInVisualStudio=true` includes them.
- **Add-on fixes** in 1.12.3, merged to `attixray/bari-vs-addon` `master` by
  attixray/bari-vs-addon#1 and built by its new `Build VSIX` workflow
  (run 36111077961, green):
  - the solution watcher opens files with `FileShare.Delete`, treats files
    that vanish or cannot be read as absent instead of faulting the check,
    and ignores `*_wpftmp.*` (see H3);
  - bari's stderr is drained; it was redirected and never read, so a bari
    writing enough to it would block and the build would never finish.
- **Add-on 1.12.4–1.12.6**, merged by attixray/bari-vs-addon#2:
  - the package is an `AsyncPackage` that loads in the background, so
    Visual Studio's synchronous autoload no longer has to be allowed
    (Visual Studio 18.10 has no UI for that setting any more);
  - Cancel terminates a job object that holds bari and everything it starts,
    instead of walking the process tree with WMI while bari keeps running;
  - log4net 2.0.8 → 3.4.0.

## Coverage

| | H1 | H2 | H3 | H4 |
|---|---|---|---|---|
| Codex through this bridge | yes, with the variable set | yes | reads during builds removed | yes |
| Claude Code `csharp-lsp` plugin | yes, if its process has the variable | depends on Claude Code's file watching; not changed here | not changed | not changed |
| Visual Studio builds (add-on → bari, or project-level) vs. language servers | yes, with the variable set | yes (no project regeneration; wpftmp ignored) | yes: reference copies; add-on watcher fixed | n/a |
| Visual Studio's own design-time builds | only with `DesignTimeIsolationInVisualStudio=true` | no | no | n/a |
| Two real builds at once (VS project build during a bari build) | no: they share one directory | | | |

## Rejected options

- **Build-quiet mode from a marker file, mutex or running `bari.exe`.**
  - A marker or mutex needs bari changes or a wrapper.
  - Process detection matches builds of other working copies, and it cannot
    tell when a build ends: MSBuild nodes outlive builds.
  - The gate needs no build signal: identical regeneration is not a change.
- **Watching `target/` to detect the end of a build.** A watch holds a handle
  on every directory it covers. A handle on a deleted directory keeps it
  pending and makes bari's recursive delete fail with "The directory is not
  empty".
- **Restarting csharp-ls after builds.** A restart is a full design-time load,
  which is the operation that collides, and the end of a build still cannot
  be detected.
- **Pinning `--solution target/sp-jd.sln`.** The file disappears on clean.
  Pinning changes nothing about design-time writes.
- **`Directory.Build.targets` in the Suite.** It is imported after
  `Microsoft.Common.CurrentVersion.targets`, too late for properties derived
  during evaluation (`TargetRefPath`, the assembly attributes file).

## bari changes (attixray/bari 1.1.0)

The owner approved the bari follow-ups. They are in the fork `attixray/bari`,
which builds on `p5ych08illy/bari`'s .NET 10 migration, and are released as
[1.1.0](https://github.com/attixray/bari/releases/tag/1.1.0):

| change | PR | effect |
|---|---|---|
| write `.csproj`, `.fsproj`, `.vcxproj` and `.sln` only when their content changes | attixray/bari#1 | an up-to-date build touches no project file; the churn is gone at the source, for every watcher |
| derive new project GUIDs from the suite and project names | attixray/bari#1 | a clean regenerates byte-identical project files |
| retry deletes that another process briefly blocks, for about 3 s, and name the holder through the Restart Manager | attixray/bari#1 | a brief lock no longer fails a clean; a lasting one says who holds it |
| keep `target/.vs` on clean | attixray/bari#2 | Visual Studio's DevHub index no longer fails every clean, and the solution state survives |
| stop writing when the console output is closed | attixray/bari#3 | no crash while reporting an error after the reader went away |
| write `target/<solution>.yaml` only when it changes | attixray/bari#3 | `bari vs` no longer touches the add-on's file |
| version from `git describe --tags --long` | attixray/bari#4 | builds report `1.1.0.<n>` instead of `0.0.0.0` |

Not done: generating the intermediate paths conditioned on
`'$(DesignTimeBuild)' == 'true'`, which would protect every tool without an
environment variable. `DesignTimeIsolation.targets` covers it for now.

## Local verification (2026-09-25)

The owner ran two handoff tests on the Suite (`sp-jd`, `debug-x64`) with
Visual Studio Professional 2026 18.10 and synchronous autoload off. The
second used bari `777d57e` (1.1.0 without the version fix) in a separate
clone, next to the old `C:\Bari`.

| check | add-on 1.12.4, bari 1.0.3.68 | add-on 1.12.6, bari 1.1.0 |
|---|---|---|
| build of `sp-jd` (C#, F#, C++/CLI, NuGet, Python postprocessors) | pass | pass: 91 s first build, 3 s up to date |
| add-on load with no solution open | 2 s | 1.9 s |
| add-on load after a direct `.sln` open | about 60 s | about 61 s (see residual risks) |
| Cancel | about 21 s; bari crashed writing to the closed pipe | whole tree gone within 0.6 s of bari; clean Build pane |
| clean + rebuild ×3 with the solution open | every step warned: DevHub held a `target\.vs\…\*.vsidx` | no warning; `target\.vs` kept |
| project files after a clean | new `<ProjectGuid>` in each | 99 files byte-identical, and the `.sln` |
| repeated builds in Visual Studio | not checked | no timestamp changed, no reload bar |
| brief lock during a clean | not checked | deleted after 4 retries |
| lasting lock during a clean | not checked | the warning names `powershell (<pid>)` |

The one failure in the 1.12.4 run was a `CS2001` for a `*_wpftmp` project
during a rebuild started from Visual Studio, while about 13 of Visual
Studio's own design-time MSBuild nodes ran. That is H1 with Visual Studio as
the design-time builder, which the targets file covers only with
`DesignTimeIsolationInVisualStudio=true`. The 1.12.6 run showed no CS2001.

## Residual risks

- csharp-ls lists every `*.sln` under the workspace on load
  (`Directory.GetFiles(..., AllDirectories)`), and design-time output now
  lives under `target/tmp/**/designtime`. A load that starts during
  `bari clean` can still make the recursive delete of `target/` report
  "The directory is not empty". bari reports this as a warning.
- A genuine project change reaches csharp-ls `--project-settle` plus 5 s after
  the last project-file write. Until then, queries use the previous project
  structure.
- If the variable is set user-wide, removing the file silently turns the
  isolation off: MSBuild skips a missing file. Keep the file at a stable path.
- Visual Studio holds background package loads until a solution opened at
  start-up has finished loading, about a minute for `sp-jd`. A build clicked
  before that can run as an ordinary MSBuild build instead of through bari.
  Only synchronous autoload would change that. In the test, the build clicked
  during the load still went through bari.
- Only a unit test covers the fix for bari crashing on a closed console. The
  local test could not reproduce the crash with the old bari either, and the
  add-on's job-object Cancel no longer leaves bari running against a closed
  pipe.

## Questions for the owner

1. Was Visual Studio open on the Suite on 2026-09-24 around 17:23 and 17:33?
   **Most likely yes**, which accounts for the `*.g.i.cs` files.
2. Does the Suite root have a `.gitignore` with `*.csproj`? **No**, so the
   bridge sees every project event, as in the reproduction.
3. Does bari write identical project files on every build? From the source:
   yes, except `<ProjectGuid>` after a clean (see H2). The acceptance steps
   check the rest.
4. Which bari revision is `C:\Bari\bari.exe` 1.0.3.68? **`9386ad0`**, by its
   build time (see H2).
5. Which of the bari follow-ups to make? **All but the conditional
   intermediate paths**; see [bari changes](#bari-changes-attixraybari-110).

## Installation (owner)

1. **Download and verify the release.** In PowerShell:

   ```powershell
   $version = 'v0.1.1-attixray.3'
   $root = "$env:USERPROFILE\.local\share\mcp-language-server"
   $base = "https://github.com/attixray/mcp-language-server/releases/download/$version"
   $zip = "mcp-language-server_${version}_windows_amd64.zip"
   Invoke-WebRequest "$base/$zip" -OutFile "$env:TEMP\$zip"
   Invoke-WebRequest "$base/SHA256SUMS" -OutFile "$env:TEMP\SHA256SUMS"
   $expected = ((Get-Content "$env:TEMP\SHA256SUMS") -match [regex]::Escape($zip)).Split(' ')[0]
   if ((Get-FileHash "$env:TEMP\$zip").Hash -ne $expected) { throw 'checksum mismatch' }
   Expand-Archive "$env:TEMP\$zip" -DestinationPath "$root\$version" -Force
   ```

2. **Turn on design-time isolation for every language server.** Copy the targets
   file to a path that does not change with versions, and set the variable for
   your user:

   ```powershell
   Copy-Item "$root\$version\DesignTimeIsolation.targets" "$root\DesignTimeIsolation.targets"
   [Environment]::SetEnvironmentVariable('CustomBeforeMicrosoftCommonTargets', "$root\DesignTimeIsolation.targets", 'User')
   ```

   - New processes inherit the variable: Codex, Claude Code and its
     `csharp-lsp` plugin, and Visual Studio. Restart them after setting it.
   - bari's builds see the variable too. The file changes nothing unless
     `DesignTimeBuild` is true.
   - To limit it to the bridge instead, add the same value under
     `[mcp_servers.csharp-lsp.env]` in `C:\Actuals\Suite\.codex\config.toml`.
     Claude Code's plugin then stays unprotected against H1.

3. **Point Codex at the new binary.** In `C:\Actuals\Suite\.codex\config.toml`:

   ```toml
   command = 'C:\Users\Attila\.local\share\mcp-language-server\v0.1.1-attixray.3\mcp-language-server.exe'
   ```

   The arguments stay as they are. `--project-settle 30s` is the default.

4. **Install the add-on (1.12.6).** Download `BariVSPackage.vsix` from the
   [v1.12.6 release](https://github.com/attixray/bari-vs-addon/releases/tag/v1.12.6),
   close Visual Studio, and open the file from an elevated prompt. It installs
   for all users and replaces an all-users 1.12.2 or later. Uninstall a
   per-user copy older than 1.12.2 first; the add-on README has the details.

5. **Install bari 1.1.0.** It is framework-dependent and needs the .NET 10
   runtime (`dotnet --list-runtimes` shows `Microsoft.NETCore.App 10.*`).
   - Download `bari-1.1.0-net10.zip` from the
     [1.1.0 release](https://github.com/attixray/bari/releases/tag/1.1.0) and
     check it against `SHA256SUMS`.
   - Replace the whole `C:\Bari` directory with its contents. Keep the old
     directory as a fallback.
   - Run `C:\Bari\bari.exe --target debug-x64 clean` once. The old and the new
     bari must not share a `cache\`.
   - Run `C:\Bari\bari.exe --target debug-x64 vs sp-jd`, so the solution's
     `.yaml` names the new `bari-path`.

6. **Optional, while running the acceptance steps:** add
   `LOG_LEVEL = 'INFO'` and
   `LOG_FILE = 'C:\Users\Attila\.local\share\mcp-language-server\bridge.log'`
   under `[mcp_servers.csharp-lsp.env]`. The log then shows every
   `will reload solution` request csharp-ls makes.

## Acceptance (owner)

1. Open the Suite solution in Visual Studio with the fixed add-on, and start
   one Codex session and one Claude Code session on `C:\Actuals\Suite`. In
   each session, ask for a definition, so csharp-ls has loaded the solution.
2. Confirm that isolation is active: this count should be above zero.

   ```powershell
   (Get-ChildItem C:\Actuals\Suite\target\tmp -Recurse -Directory -Filter designtime).Count
   ```

3. Record the project files, then run the sequence three times, keeping the
   output:

   ```powershell
   # Hash each project file without <ProjectGuid>, as the bridge compares them.
   function Get-ProjectHashes {
     Get-ChildItem C:\Actuals\Suite\src -Recurse -Filter *.csproj | ForEach-Object {
       $text = [IO.File]::ReadAllText($_.FullName) -replace '<ProjectGuid>[^<]*</ProjectGuid>', ''
       $stream = [IO.MemoryStream]::new([Text.Encoding]::UTF8.GetBytes($text))
       [pscustomobject]@{ Path = $_.FullName; Hash = (Get-FileHash -InputStream $stream).Hash }
     }
   }
   $before = Get-ProjectHashes
   1..3 | ForEach-Object {
     C:\Bari\bari.exe --target debug-x64 -v clean
     C:\Bari\bari.exe --target debug-x64 -v build sp-jd
     C:\Bari\bari.exe --target debug-x64 -v rebuild sp-jd
   } *>&1 | Tee-Object "$env:TEMP\bari-acceptance.log"
   Select-String 'CS2001|BG1002|being used by another process|Failed to clean target root' "$env:TEMP\bari-acceptance.log"
   $after = Get-ProjectHashes
   Compare-Object $before $after -Property Path, Hash
   ```

   - `Select-String` should print nothing.
   - `Compare-Object` should print nothing. That confirms that bari
     regenerates identical project files apart from `<ProjectGuid>`, which the
     bridge relies on to report no change. With bari 1.1.0 the files are
     identical, `<ProjectGuid>` included.

4. Run `C:\Bari\bari.exe --target debug-x64 test sp-jd`. It should pass.
5. Ask both sessions for a definition and references of a symbol used across
   projects. The answers should be correct.
6. If a sharing violation still appears, capture the holder at once, e.g.
   `handle.exe -a -u <file>` (Sysinternals), and check the bridge log for
   reload requests around that time.
7. End the Codex session. Check with `Get-Process csharp-ls` that its
   language server is gone.

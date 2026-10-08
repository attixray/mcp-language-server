# MCP Language Server

Fork binaries for Windows, Linux and macOS are available from
[attixray releases](https://github.com/attixray/mcp-language-server/releases).
See [RELEASING.md](RELEASING.md) for automated builds, checksums and release tags.

[![Go Tests](https://github.com/isaacphi/mcp-language-server/actions/workflows/go.yml/badge.svg)](https://github.com/isaacphi/mcp-language-server/actions/workflows/go.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/isaacphi/mcp-language-server)](https://goreportcard.com/report/github.com/isaacphi/mcp-language-server)
[![GoDoc](https://pkg.go.dev/badge/github.com/isaacphi/mcp-language-server)](https://pkg.go.dev/github.com/isaacphi/mcp-language-server)
[![Go Version](https://img.shields.io/github/go-mod/go-version/isaacphi/mcp-language-server)](https://github.com/isaacphi/mcp-language-server/blob/main/go.mod)

This is an [MCP](https://modelcontextprotocol.io/introduction) server that runs and exposes a [language server](https://microsoft.github.io/language-server-protocol/) to LLMs. Not a language server for MCP, whatever that would be.

## Demo

`mcp-language-server` helps MCP enabled clients navigate codebases more easily by giving them access semantic tools like get definition, references, rename, and diagnostics.

![Demo](demo.gif)

## Setup

1. **Install Go**: Follow instructions at <https://golang.org/doc/install>
2. **Install or update this server**: `go install github.com/isaacphi/mcp-language-server@latest`
3. **Install a language server**: _follow one of the guides below_
4. **Configure your MCP client**: _follow one of the guides below_

<details>
  <summary>Go (gopls)</summary>
  <div>
    <p><strong>Install gopls</strong>: <code>go install golang.org/x/tools/gopls@latest</code></p>
    <p><strong>Configure your MCP client</strong>: This will be different but similar for each client. For Claude Desktop, add the following to <code>~/Library/Application\ Support/Claude/claude_desktop_config.json</code></p>

<pre>
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": ["--workspace", "/Users/you/dev/yourproject/", "--lsp", "gopls"],
      "env": {
        "PATH": "/opt/homebrew/bin:/Users/you/go/bin",
        "GOPATH": "/users/you/go",
        "GOCACHE": "/users/you/Library/Caches/go-build",
        "GOMODCACHE": "/Users/you/go/pkg/mod"
      }
    }
  }
}
</pre>

<p><strong>Note</strong>: Not all clients will need these environment variables. For Claude Desktop you will need to update the environment variables above based on your machine and username:</p>
<ul>
  <li><code>PATH</code> needs to contain the path to <code>go</code> and to <code>gopls</code>. Get this with <code>echo $(which go):$(which gopls)</code></li>
  <li><code>GOPATH</code>, <code>GOCACHE</code>, and <code>GOMODCACHE</code> may be different on your machine. These are the defaults.</li>
</ul>

  </div>
</details>
<details>
  <summary>Rust (rust-analyzer)</summary>
  <div>
    <p><strong>Install rust-analyzer</strong>: <code>rustup component add rust-analyzer</code></p>
    <p><strong>Configure your MCP client</strong>: This will be different but similar for each client. For Claude Desktop, add the following to <code>~/Library/Application\ Support/Claude/claude_desktop_config.json</code></p>

<pre>
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace",
        "/Users/you/dev/yourproject/",
        "--lsp",
        "rust-analyzer"
      ]
    }
  }
}
</pre>
  </div>
</details>
<details>
  <summary>Python (pyright)</summary>
  <div>
    <p><strong>Install pyright</strong>: <code>npm install -g pyright</code></p>
    <p><strong>Configure your MCP client</strong>: This will be different but similar for each client. For Claude Desktop, add the following to <code>~/Library/Application\ Support/Claude/claude_desktop_config.json</code></p>

<pre>
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace",
        "/Users/you/dev/yourproject/",
        "--lsp",
        "pyright-langserver",
        "--",
        "--stdio"
      ]
    }
  }
}
</pre>
  </div>
</details>
<details>
  <summary>Typescript (typescript-language-server)</summary>
  <div>
    <p><strong>Install typescript-language-server</strong>: <code>npm install -g typescript typescript-language-server</code></p>
    <p><strong>Configure your MCP client</strong>: This will be different but similar for each client. For Claude Desktop, add the following to <code>~/Library/Application\ Support/Claude/claude_desktop_config.json</code></p>

<pre>
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace",
        "/Users/you/dev/yourproject/",
        "--lsp",
        "typescript-language-server",
        "--",
        "--stdio"
      ]
    }
  }
}
</pre>
  </div>
</details>
<details>
  <summary>C/C++ (clangd)</summary>
  <div>
    <p><strong>Install clangd</strong>: Download prebuilt binaries from the <a href="https://github.com/clangd/clangd/releases">official LLVM releases page</a> or install via your system's package manager (e.g., <code>apt install clangd</code>, <code>brew install clangd</code>).</p>
    <p><strong>Configure your MCP client</strong>: This will be different but similar for each client. For Claude Desktop, add the following to <code>~/Library/Application\\ Support/Claude/claude_desktop_config.json</code></p>

<pre>
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace",
        "/Users/you/dev/yourproject/",
        "--lsp",
        "/path/to/your/clangd_binary",
        "--",
        "--compile-commands-dir=/path/to/yourproject/build_or_compile_commands_dir"
      ]
    }
  }
}
</pre>
    <p><strong>Note</strong>:</p>
    <ul>
      <li>Replace <code>/path/to/your/clangd_binary</code> with the actual path to your clangd executable.</li>
      <li><code>--compile-commands-dir</code> should point to the directory containing your <code>compile_commands.json</code> file (e.g., <code>./build</code>, <code>./cmake-build-debug</code>).</li>
      <li>Ensure <code>compile_commands.json</code> is generated for your project for clangd to work effectively.</li>
    </ul>
  </div>
</details>
<details>
  <summary>Other</summary>
  <div>
    <p>I have only tested this repo with the servers above but it should be compatible with many more. Note:</p>
    <ul>
      <li>The language server must communicate over stdio.</li>
      <li>Any aruments after <code>--</code> are sent as arguments to the language server.</li>
      <li>Any env variables are passed on to the language server.</li>
    </ul>
  </div>
</details>

### Install with a coding agent

Paste one of these prompts into a coding agent (Claude Code, Codex or similar)
started in the repository you want to navigate. The agent installs the
language server and the newest release of this fork, registers the bridge
with itself for that repository, and documents it in the repository's
`HACKING.md`. It does not commit.

<details>
  <summary>C/C++ (clangd)</summary>

```text
Set up semantic C/C++ code navigation for this repository through an MCP
server, then document it. Ask me before anything that needs admin rights.

1. clangd: if `clangd --version` works, use that clangd. Otherwise install
   it with the system package manager (apt install clangd, brew install llvm,
   winget install LLVM.LLVM) or from the newest zip at
   https://github.com/clangd/clangd/releases. Note the binary's full path.

2. compile_commands.json: find it (repository root, build/, out/,
   cmake-build-*). If there is none, ask me before creating it: for CMake,
   configure a build directory with -DCMAKE_EXPORT_COMPILE_COMMANDS=ON; for
   other build systems, ask me which tool to use (for example bear).

3. The bridge: find the newest release of attixray/mcp-language-server,
   prereleases included (`gh release list -R attixray/mcp-language-server
   --limit 1`, or https://api.github.com/repos/attixray/mcp-language-server/releases?per_page=1;
   /releases/latest skips prereleases). From it download SHA256SUMS and
   mcp-language-server_<tag>_<os>_<arch>.zip on Windows or .tar.gz elsewhere
   (os: windows, linux or darwin; arch: amd64 or arm64). Stop if the
   archive's SHA-256 does not match its line in SHA256SUMS. Unpack it into
   ~/.local/share/mcp-language-server/<tag>/ (on Windows
   %USERPROFILE%\.local\share\mcp-language-server\<tag>\). Go is not needed.

4. Register the bridge with the MCP client you are running in, for this
   repository only, under the name cpp-lsp:
   - command: the unpacked mcp-language-server binary, absolute path;
   - args: --workspace <repository root> --lsp <clangd path> --
     --compile-commands-dir=<directory of compile_commands.json>
   Claude Code: claude mcp add --scope project cpp-lsp -- <command> <args>.
   Codex: a [mcp_servers.cpp-lsp] table in .codex/config.toml.

5. Add or update a section "Code navigation (cpp-lsp)" in HACKING.md at the
   repository root. Keep the rest of the file; create it if it is missing.
   The section says:
   - what was installed, the versions, the paths, and where it is registered;
   - to prefer these tools to text search: definition, references,
     references_at (file, 1-based line and column), hover, diagnostics,
     rename_symbol, edit_file;
   - that answers are only as good as compile_commands.json, and when to
     regenerate it (new files, changed build options);
   - how to update: repeat step 3 with the newer tag and change the
     registered path; update clangd the way it was installed.

6. If the cpp-lsp tools are available in this session, call definition on a
   symbol of this repository and diagnostics on one source file, and show
   me the results. Otherwise tell me to restart the client and what to try.
   Do not commit; list the files you changed.
```

</details>
<details>
  <summary>C# (csharp-ls)</summary>

```text
Set up semantic C# code navigation for this repository through an MCP
server, then document it. Ask me before anything that needs admin rights.

1. .NET SDK: `dotnet --list-sdks` must list one. If it does not, stop and
   tell me.

2. csharp-ls: run `dotnet tool update --global csharp-ls`, which installs or
   updates it. It goes to ~/.dotnet/tools (on Windows
   %USERPROFILE%\.dotnet\tools). Note the binary's full path.

3. Solution: csharp-ls loads the solution it finds in the workspace. If the
   repository has several .sln or .slnx files, or a build tool generates the
   solution, ask me which directory to use as the workspace.

4. The bridge: find the newest release of attixray/mcp-language-server,
   prereleases included (`gh release list -R attixray/mcp-language-server
   --limit 1`, or https://api.github.com/repos/attixray/mcp-language-server/releases?per_page=1;
   /releases/latest skips prereleases). From it download SHA256SUMS and
   mcp-language-server_<tag>_<os>_<arch>.zip on Windows or .tar.gz elsewhere
   (os: windows, linux or darwin; arch: amd64 or arm64). Stop if the
   archive's SHA-256 does not match its line in SHA256SUMS. Unpack it into
   ~/.local/share/mcp-language-server/<tag>/ (on Windows
   %USERPROFILE%\.local\share\mcp-language-server\<tag>\). Go is not needed.
   Copy DesignTimeIsolation.targets from the archive to
   ~/.local/share/mcp-language-server/DesignTimeIsolation.targets, a path
   that stays the same across versions.

5. Register the bridge with the MCP client you are running in, for this
   repository only, under the name csharp-lsp:
   - command: the unpacked mcp-language-server binary, absolute path;
   - args: --workspace <workspace> --lsp <csharp-ls path>
   - env: CustomBeforeMicrosoftCommonTargets=<absolute path of the copied
     DesignTimeIsolation.targets>. It keeps csharp-ls's design-time builds
     from replacing files that a build running at the same time uses; it
     changes nothing else.
   Claude Code: claude mcp add --scope project csharp-lsp
   -e CustomBeforeMicrosoftCommonTargets=<path> -- <command> <args>.
   Codex: [mcp_servers.csharp-lsp] and [mcp_servers.csharp-lsp.env] tables in
   .codex/config.toml.

6. Add or update a section "Code navigation (csharp-lsp)" in HACKING.md at
   the repository root. Keep the rest of the file; create it if it is
   missing. The section says:
   - what was installed, the versions, the paths, and where it is registered;
   - to prefer these tools to text search: definition, references,
     references_at (file, 1-based line and column), hover, diagnostics,
     rename_symbol, edit_file;
   - that the first answers wait until csharp-ls has loaded the solution,
     and that after project or solution files change, csharp-ls reloads them
     only once they have been unchanged for 30 seconds (--project-settle);
   - how to update: `dotnet tool update --global csharp-ls`, and for the
     bridge repeat step 4 with the newer tag and change the registered path.

7. If the csharp-lsp tools are available in this session, call definition on
   a type of this repository and diagnostics on one .cs file, and show me
   the results. Otherwise tell me to restart the client and what to try.
   Do not commit; list the files you changed.
```

</details>

## .NET projects built by other tools

Project and solution files (`.csproj`, `.sln`, `.slnx`, `.props`, `.targets`
and the like) make .NET language servers reload the whole solution. When a
build tool regenerates them, the bridge holds their events until the files have
stayed unchanged for `--project-settle` (default `30s`). It then reports only
content changes, all in one notification. The WPF markup compiler's temporary
`*_wpftmp.*` projects and `obj/` directories are ignored.

Design-time builds (csharp-ls, Visual Studio) write into a project's
intermediate directory. If a project sets that directory unconditionally, point
`CustomBeforeMicrosoftCommonTargets` at `DesignTimeIsolation.targets`. The file
is included in the release archives and lives in `contrib/msbuild`. Set the
variable in the language server's environment, so its design-time builds do not
replace files a concurrent command-line build is using. See
[docs/bari-coexistence.md](docs/bari-coexistence.md).

On Windows the language server and its child processes exit with their owning
broker, even when it is killed. Each stdio adapter exits with its parent; the
shared broker remains available to other adapters until its idle timeout.
`--shared=false` retains the dedicated bridge/child lifetime.

## Shared LSP and recovery

Adapters now share one supervised LSP per workspace/configuration by default.
See [shared LSP and recovery](docs/shared-lsp.md) for ownership locks, deadlines,
diagnostics, restart limits and configuration. Existing stdio MCP configurations
continue to work after updating the executable.

## Tools

- `lsp_status`: Reports shared LSP health, active/queued requests and restart budget.
- `definition`: Retrieves the complete source code definition of any symbol (function, type, constant, etc.) from your codebase.
- `references`: Locates all usages and references of a symbol throughout the codebase.
- `references_at`: Finds references using `filePath`, `line` and `column` instead of searching the workspace by name. Line and column are 1-based; column counts UTF-16 code units. Use `LSP_CONTEXT_LINES=0` to skip enclosing-symbol lookups and return only reference lines. Existing `references` calls remain supported.
- `diagnostics`: Provides diagnostic information for a specific file, including warnings and errors. Synchronizes saved content first, uses pull diagnostics when advertised, and otherwise waits for a published report (up to 30 seconds or the caller's deadline). Missing reports and server errors are reported as errors, not as a clean file. `contextLines` is a non-negative number (default 5). Versioned publications must match the synchronized file; servers omitting the version can only be checked by arrival order.
- `hover`: Display documentation, type hints, or other hover information for a given location.
- `rename_symbol`: Rename a symbol across a project.
- `edit_file`: Allows making multiple text edits to a file based on line numbers. Provides a more reliable and context-economical way to edit files compared to search and replace based edit tools.

## About

This codebase makes use of edited code from [gopls](https://go.googlesource.com/tools/+/refs/heads/master/gopls/internal/protocol) to handle LSP communication. See ATTRIBUTION for details. Everything here is covered by a permissive BSD style license.

[mcp-go](https://github.com/mark3labs/mcp-go) is used for MCP communication. Thank you for your service.

This is beta software. Please let me know by creating an issue if you run into any problems or have suggestions of any kind.

## Contributing

Please keep PRs small and open Issues first for anything substantial. AI slop O.K. as long as it is tested, passes checks, and doesn't smell too bad.

### Setup

Clone the repo:

```bash
git clone https://github.com/isaacphi/mcp-language-server.git
cd mcp-language-server
```

A [justfile](https://just.systems/man/en/) is included for convenience:

```bash
just -l
Available recipes:
    build    # Build
    check    # Run code audit checks
    fmt      # Format code
    generate # Generate LSP types and methods
    help     # Help
    install  # Install locally
    snapshot # Update snapshot tests
    test     # Run tests
```

Configure your Claude Desktop (or similar) to use the local binary:

```json
{
  "mcpServers": {
    "language-server": {
      "command": "/full/path/to/your/clone/mcp-language-server/mcp-language-server",
      "args": [
        "--workspace",
        "/path/to/workspace",
        "--lsp",
        "language-server-executable"
      ],
      "env": {
        "LOG_LEVEL": "DEBUG"
      }
    }
  }
}
```

Rebuild after making changes.

### Logging

Setting the `LOG_LEVEL` environment variable to DEBUG enables verbose logging to stderr for all components including messages to and from the language server and the language server's logs.

### LSP interaction

- `internal/lsp/methods.go` contains generated code to make calls to the connected language server.
- `internal/protocol/tsprotocol.go` contains generated code for LSP types. I borrowed this from `gopls`'s source code. Thank you for your service.
- LSP allows language servers to return different types for the same methods. Go doesn't like this so there are some ugly workarounds in `internal/protocol/interfaces.go`.

### Local Development and Snapshot Tests

There is a snapshot test suite that makes it a lot easier to try out changes to tools. These run actual language servers on mock workspaces and capture output and logs.

You will need the language servers installed locally to run them. There are tests for go, rust, python, and typescript.

```
integrationtests/
├── tests/        # Tests are in this folder
├── snapshots/    # Snapshots of tool outputs
├── test-output/  # Gitignored folder showing the final state of each workspace and logs after each test run
└── workspaces/   # Mock workspaces that the tools run on
```

To update snapshots, run `UPDATE_SNAPSHOTS=true go test ./integrationtests/...`

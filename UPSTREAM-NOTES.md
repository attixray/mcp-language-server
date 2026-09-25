# Selected upstream fixes

The fork retains its tested Windows URI handling, serialized LSP writes and
single initialization notification. Those overlap upstream PRs
[#82](https://github.com/isaacphi/mcp-language-server/pull/82),
[#151](https://github.com/isaacphi/mcp-language-server/pull/151) and
[#49](https://github.com/isaacphi/mcp-language-server/pull/49); they were not
replaced or applied twice.

After reviewing upstream proposals, this fork independently adapts these ideas:

- [#141](https://github.com/isaacphi/mcp-language-server/pull/141): synchronize
  every open buffer before rename, fail on synchronization errors, skip unchanged
  buffers, and reject disk changes detected while the rename request was running.
- [#140](https://github.com/isaacphi/mcp-language-server/pull/140) and
  [#150](https://github.com/isaacphi/mcp-language-server/pull/150): idempotent
  cleanup, shutdown on stdio EOF, and a force-close/kill backstop started before
  potentially blocking writes. Drain stderr in bounded chunks even for long lines.
  Existing context cancellation and frame serialization are preserved.
- [#139](https://github.com/isaacphi/mcp-language-server/pull/139): reject virtual
  document URIs safely, decode legacy hover forms, preserve final-line hover
  fallback, and keep server-provided names out of formatting templates.
- [#80](https://github.com/isaacphi/mcp-language-server/pull/80): avoid advertising
  MCP logging/setLevel support that the current dependency does not implement.
  Local stderr logging through LOG_LEVEL remains available.

These are selective adaptations, not wholesale PR cherry-picks. The upstream
authors' reports identified additional failure cases; tests cover the fork's
implementation using real stdio child processes and synthetic workspace edits.

Rename preflight is not a filesystem transaction: external writes after the
final check, or changes to unopened documents during planning, remain outside
this guarantee. The tool does not provide atomic rollback of multi-file edits.

Deferred features include multi-LSP sessions, call hierarchy/implementation
tools, asynchronous startup, and platform-specific watch descriptor budgets.
EOF is handled once the stdio server starts; slow initialization remains subject
to the host's startup timeout. The shutdown backstop terminates the direct LSP
child. On Windows, a kill-on-close job object also terminates its descendants
when the bridge exits, however it exits; elsewhere, descendant processes
started by third-party wrappers are not tracked.

# Shared LSP ownership, diagnostics and recovery

The default `--shared=true` mode connects each stdio MCP adapter to one broker
per canonical workspace/worktree and effective LSP configuration. The broker
owns one LSP process, watcher, document-version history and serialized tool
queue. Clients keep separate MCP sessions and request-ID/cancellation spaces.

```text
agent MCP adapter ---+
agent MCP adapter ---+--- authenticated loopback socket --- broker --- LSP
agent MCP adapter ---+
```

## Ownership and lifetime (A)

The broker uses a lifetime kernel lock (`LockFileEx` on Windows, `flock` on
Linux/macOS). A stale descriptor or heartbeat never authorizes takeover; only
acquiring the OS lock does. The `.lock` file remains after shutdown and must
not be deleted while processes run. A simultaneous-start loser never launches
an LSP. Follower processes verify a live owner's authenticated readiness;
a readable stale descriptor does not end the lock wait. An adapter retries an
exited startup child at most three times within the combined startup budget.

Connections use a random 256-bit token in a per-user descriptor and listen only
on `127.0.0.1`. Loopback TCP provides the same implementation on all three target
platforms; this is not an externally accessible MCP service. Unix directories
and state files use owner-only permissions; Windows uses the user-cache ACLs.
Each configuration has a separate descriptor/log. The key includes canonical
workspace and LSP paths, arguments, environment (excluding `CODEX_*` IDs and
shell bookkeeping), executable build identity and supervision settings.
Different worktrees or effective configurations cannot share document state.

Tool operations are serialized, including document synchronization and workspace
edits. Disconnecting one adapter cancels its requests without killing other
clients' LSP. Active response waits send `$/cancelRequest`, ignore late replies,
and preserve the shared process. A started frame finishes under its original
deadline even if its session disconnects; an actually blocked write can still
abort that generation. The broker stops after the last connection has been gone
for the idle timeout. Shared brokers are independent of the first adapter's parent;
adapters themselves still monitor their parent and stdin EOF.

External editors/build tools do not participate in this queue. Rename validates
its disk snapshots, but cannot make external filesystem writers transactional.
The broker prevents duplicate LSPs from this bridge; it does not lock out an
editor's independent LSP or solve that server's external cache/build locks.

## Deadlines and diagnostics (B)

| Option | Default | Purpose |
| --- | --- | --- |
| `--shared` | `true` | Share a supervised broker |
| `--request-timeout` | `60s` | Total tool budget, including queueing/recovery |
| `--init-timeout` | `120s` | LSP initialization and document restore budget |
| `--lock-timeout` | `15s` | Broker ownership/startup contention budget |
| `--idle-timeout` | `2m` | Broker lifetime after last disconnect |
| `--restart-limit` | `3` | Maximum restart attempts per window; `0` disables |
| `--restart-window` | `5m` | Rolling restart-accounting window |
| `--broker-dir` | per-user cache | Override state/log/diagnostic directory |

Discovery/startup has a combined `lock-timeout + init-timeout` budget. The tool
budget includes waiting behind other agents and recovering a failed LSP.
Waiting for a writer/file-sync gate observes cancellation. A blocked OS pipe
write closes that generation's stream and process tree, instead of abandoning
a goroutine that could later emit a partial frame. EOF releases pending calls.

On a tool deadline the supervisor allows up to two additional seconds for the
worker to stop. Before generation replacement it also joins the receive loop,
including server-side `workspace/applyEdit` callbacks, with a two-second grace
period. A worker or server callback still stuck beyond that grace period
quarantines the broker; it will return errors rather than risk a mutation overlapping another
generation. `lsp_status` remains usable and reports this condition.

`lsp_status` reports broker/LSP PIDs, generation, active/queued tools,
outstanding request methods and phases, restart budget and state location.
Status is available during another tool's stall or exhausted restart budget.
An open `healthy_transport` is not proof of server progress.

Timeout/transport failures save JSON state and a goroutine dump (at most 1 MiB)
in `<broker-dir>/diagnostics/`, retaining 20 snapshots. JSON captures request IDs,
methods, phases and timing, including the state at transport abort; it excludes
document contents. Logs go to `<broker-dir>/<configuration-hash>.log`.
Existing `LOG_LEVEL`, `LOG_FILE` and wire-log controls still apply; wire logs may
contain document contents when explicitly enabled.

## Supervised recovery (C)

An independent watchdog can close the transport without acquiring the tool,
file-sync or writer gates. It detects outstanding tool deadlines and a closed
LSP transport. Before replacing a generation it cancels the old watcher,
terminates/reaps the old LSP and waits for the tool worker, server callbacks
and watcher to stop.

Restart attempts use exponential backoff (1, 2, 4 seconds, capped at 16) and a
rolling limit. Each attempt consumes the budget even if initialization fails.
The new LSP is initialized and open files are reloaded from their current disk
contents; deleted files are skipped. Failed restores retain the full restore
set for the next attempt. Diagnostics are cleared with the old client.

The interrupted request returns an error. No tool operation is replayed across
a generation change. Callers can retry reads; they must verify disk state before
retrying a rename/edit whose result is uncertain. Exhausting the restart budget
returns explicit errors; automatic recovery resumes when the window permits
another attempt. `--restart-limit=0` disables automatic restarts.

Windows LSPs start suspended, are assigned to a generation-specific job object,
and then resume, so build hosts cannot escape during startup. Killing that
job kills the full generation tree. A broker-level job also cleans up trees if
the broker is killed. Linux/macOS use process groups for controlled termination;
SIGKILL of the broker itself bypasses its cleanup, so use normal termination
signals on those platforms.

## Use

Existing stdio configurations work after replacing the binary. Put supervision
flags before `--`; language-server arguments still follow it:

```text
mcp-language-server --workspace C:/src/project --lsp gopls --request-timeout 60s
mcp-language-server --workspace C:/src/project --lsp clangd -- --background-index
```

`--shared=false` keeps a dedicated stdio server with the same transport deadlines,
diagnostics and watchdog. Broker-directory overrides should use a private local
directory, not a network filesystem or directory shared with other users.

## Verification

Real subprocess tests cover simultaneous agent startup, independent request-ID
spaces, active request cancellation/disconnect, stale descriptor authentication,
failed-startup retry, delayed/stuck applyEdit handlers, a silent LSP, document restore, restart limits,
mutation non-replay, bounded startup, worktree/configuration identity and kernel
lock release after owner death. Transport tests cover blocked pipe writes,
waiting writers, canceled in-progress frames, pending-call release on EOF and
response deadlines. CI runs
Linux race checks and native Windows/macOS broker tests.

# herrscher-grok-backend

**Grok Build CLI as a Herrscher backend.** It turns an inbound prompt into a
reply by running the locally installed `grok` binary headless
(`-p --output-format streaming-json`).

It is not an API client. It holds no key and reaches no xAI endpoint itself.
Authentication belongs to your `grok` install: `grok login` (browser or
`--device-auth`), or an `XAI_API_KEY` in the environment, which `grok` uses only
when no session token is stored.

Category: backend, the model edge. Ports: `contracts.Backend` and
`contracts.ResumeAware`. Gate: `tool`, every tool call goes through the host's
approval hook. Status: live.

## Install

```bash
npm install -g @xai-official/grok
grok login
herrscher plugin add github.com/Herrscherd/herrscher-grok-backend
```

Building the module needs the Go toolchain go1.27.4: `go.mod` pins it with
`toolchain go1.27.4` for the GO-2026-6859 advisory on `net/http`, and a `go`
command older than that fetches it on first use.

## Configuration

| Setting | Env | Default | What it is |
|---|---|---|---|
| `cmd` | `GROK_CMD` | `grok` | the command to run, split on spaces |
| `model` | `GROK_MODEL` | | the model a session gets when it names none, passed as `--model` |
| `effort` | `GROK_EFFORT` | | `low`, `medium`, `high` or `xhigh`, passed as `--reasoning-effort`; any other value fails the factory with `ErrInvalidSetting` |
| `dir` | `GROK_DIR` | | the working directory the CLI runs in |
| `resume` | | | the session to resume, fed by the host at construction, never set by an operator |
| `env` | | | the session environment, `K=V` per line, fed by the host at construction, never set by an operator |

The child gets the environment of the backend with the `env` setting laid over
it, a variable of the setting replacing the inherited one. That is where the
host puts `GROK_HOME` for an account profile, and `XAI_API_KEY` with
`GROK_XAI_API_BASE_URL` when its key proxy serves the `xai` provider. Grok signs
in with a stored grok.com login before any key, so the key proxy serves a turn
only when the home it runs in holds no login.

## A turn

Each turn runs one child:

```
grok -p --output-format streaming-json --always-approve --no-auto-update \
  [--model M] [--reasoning-effort E] [--resume SESSION]
```

The prompt goes on stdin, never on argv. It is the recalled memory fenced into a
`<memory data-only="true">` block, plus any downloaded attachment paths.

The child runs with `GROK_FOLDER_TRUST=0`. Grok loads project hooks, project MCP
servers and project instructions only from a trusted folder, and a headless run
cannot ask. Turning folder trust off for the child loads them without writing
the worktree into `~/.grok/trusted_folders.toml`. This is what lets the approval
hook Herrscher writes into the worktree (`.grok/hooks/herrscher-approve.json`)
run. It also means a Grok session trusts its worktree the way a Claude session
does: hooks and MCP servers the repository itself ships under `.grok/` load too.

Before every turn of a session that asks for approvals (`HERRSCHER_APPROVALS_MODE`
set and not `bypass`, with `HERRSCHER_SESSION` and `HERRSCHER_APPROVALS_BIN`, read
through `contracts.ApprovalsFromEnv`), the backend runs `grok inspect --json` in the
working directory with the turn's environment, reading at most 8 MiB of it, and runs
the turn only when that listing holds a project `pre_tool_use` hook, matching `*`,
not disabled, whose command is the herrscher approval command of that binary.
Otherwise the turn is refused before `grok` starts, with a `*GateError` that matches
`ErrGateNotLoaded` and names its `GateReason` and a remedy: the hook missing, the
hook disabled (a user who turned it off in `/hooks`), the hook calling another
herrscher binary, or an inspect that failed, printed more than 8 MiB, or printed a
listing the gate cannot read, or a policy that lets only managed hooks run. Grok
1.0.92 still lists the project hook under that policy, without `disabled`, and
skips it when a tool runs, so the gate reads the pin itself: a
`permissions.enforced` entry whose `setting` is `nonManagedHooks`. A folder Grok
does not trust leaves the hook out of the listing and reads as a missing hook.
Any of them would otherwise let every tool call run unasked. The check costs one more `grok`
process per gated turn, about 0.2 seconds.

`--always-approve` keeps a headless run from refusing every tool call it cannot
ask about. The approval policy is enforced by the `PreToolUse` hook, which runs
whatever the permission mode is, and which Herrscher writes with `failClosed`
because Grok lets a call run when a hook times out or crashes.

### Events

The stream is read line by line, each line at most 16 MiB (`LineTooLongError`).

| Grok line | Event |
|---|---|
| consecutive `text` chunks | one `text` event per block; every block after the first starts with a blank line (`\n\n`) |
| consecutive `thought` chunks | one `thinking` event per block, cut every 1 MiB |
| `tool_call` | `tool`, with a `ToolCall` in the `started` state |
| `tool_call_update` in the `completed` or `failed` status | `tool_done` |
| `plan` | `todos`, one item per entry, its `status` as the state |
| `usage` with any token | `usage` |
| `end` | `result`, with the cost, the tokens and the split by model |

A block ends at any line of another type. A block that holds only blank space
emits nothing. The reply of the turn is every text block joined, which is the
text Grok prints itself in `plain` and `json`. A reply longer than 16 MiB stops
the turn with `ReplyTooLongError` and returns no text.

A `tool_call` opens a call when its `toolCallId` is not empty, at most 256 bytes
and not seen before in the turn, its `toolName` is not empty and at most 256
bytes, and fewer than 256 calls are open and fewer than 4096 IDs seen. Any other
start is a plain `tool` event. Its detail is the first non-blank line, at most
200 runes, of the first string among `command`, `target_file`, `file_path`,
`path`, `target_directory`, `pattern`, `query`, `url` and `description` in its
input. A call that starts already finished is closed at once. An update for a
call that is not open emits nothing.

A finished call fails when its status is `failed`, with the first non-blank line
of its content as the error (`failed` when it has none), or when it is a shell
call (`rawOutput.type` `Bash`) that timed out (`timeout`) or exited with a whole
int32 code other than 0 (`exit code N`). Grok reports a shell that exited non-zero
as `completed`, so the exit code is what tells it apart. A call denied by a hook
reads `Hook denied: <reason>`. The error can carry a secret, as the detail can:
the contract asks every exporter on the host side to send it only when the
operator opts in.

### Outcome

| Stream | Respond returns |
|---|---|
| `end` with `stopReason` `end_turn` | the reply, no error |
| `end` with another stop reason | the reply so far and a `TurnFailedError` with that stop reason and the `error` text Grok gave (a hook stop) |
| `error` | the reply so far and a `TurnFailedError` with its message |
| neither, then end of file | the reply so far and a `StreamEndedError` counting the lines that were not JSON |

An unknown stop reason counts as a failure, as Grok's documentation asks. When
the stream ended well, a failing exit of the child is returned as is.

### Resume

`grok` exits after every headless turn. The `sessionId` of the `end` event is
kept, given back as `--resume` on the next turn and exposed through
`ResumeToken()`, which the host persists and feeds back through the `resume`
setting.

When a resumed turn ends on an `error` event whose message holds `no saved copy
of session`, before any event of the turn, the session is dropped, a `notice`
event with `CodeResumeLost` is emitted, and the same prompt runs once more on a
new session. Any other failure keeps the session, so a turn refused for its
login does not lose the conversation.

### Process

Each turn child starts in its own process group (`Setpgid`, in `process_unix.go`).
The group is killed with `SIGKILL` as soon as the direct child exits, and again
when a turn stops early, so a background process the CLI leaves behind dies with
the turn. A descendant that escaped the group and still holds stdout gets one
second to finish, after which the read is cut short. Outside Unix the child and
its descendants are killed as a tree with `taskkill /T /F`. stderr is discarded
unless `Verbose` is set, and it is never folded into a returned error.

## Health

`Plugin.Probe` runs `grok models` in the environment of the account probed, which
is the only command that tells the login without a turn. Its first line says it:

| First line | State | Detail | Remedy |
|---|---|---|---|
| `You are logged in with grok.com.` | `ok` | `health.connected` | |
| `You are using XAI_API_KEY.` | `ok` | `health.connected` | |
| `You are not authenticated.` | `off` | `health.not_logged_in` | run `grok login` |
| anything else | `down` | `health.unreachable` (`unreadable status`) | |
| the binary is not installed | `off` | `health.unreachable` (`command not found`) | |
| the deadline passed | `degraded` | `health.unreachable` (`timeout`) | |

`grok models` does not check a key, so `Deep` runs one turn
(`-p ... --output-format json --max-turns 1`, without `--always-approve`, on the
`model` setting, in the `dir` setting). A turn refused because the account is not
signed in or unauthorized is `down` with `health.credentials_refused` and the login
remedy, a rate limit is `degraded`, a missing `dir` is `down`, and an answered turn
keeps the report.

Account profiles relocate `GROK_HOME`. Grok copies its own binary into a new
home on first run, about 200 MB per profile.

## Model catalog

`Models` (`models.go`) is the catalogue of the Grok Build models: Grok 4.7,
Grok 4.7 Fast, and the hidden Grok 4.6 and Grok 4.5. Every entry is route
`native`, so a host under the `gateway-only` policy drops the whole catalogue.
The efforts are the ones `grok` accepts, `xhigh` excepted on Grok 4.5. The
context window is 256K, the `context_window` Grok 1.0.92 caches for each of these
models in `~/.grok/models_cache.json`. Prices are left at 0: `grok` reports the
cost of a turn in the `result` event when its server stamps one, which it does for
API key traffic and often not on a grok.com login. Without it the cost is 0.

`Plugin.Discover` adds what `grok models` lists for a signed-in account, and
nothing for an account that is not signed in.

## Not implemented

- Compaction: Grok compacts on its own when a session nears its window, and
  documents no compaction a headless caller can drive.
- `Plugin.Limits`: `grok usage` needs Grok's background service.
- Subagents: a `spawn_subagent` call shows as a tool call, with no `subagent`
  event, because the stream says nothing when the child agent ends.

## Checks

`scripts/check.sh` runs the quality chain of the go-book: tidy, verify, format,
golangci-lint, the comment check, the dash check, the race tests, a
`CGO_ENABLED=0` build and govulncheck. The fixtures under `testdata/` are real
`grok` 1.0.92 streams with their paths rewritten.

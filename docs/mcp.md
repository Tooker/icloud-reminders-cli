# Standalone Reminders MCP server

The CLI binary can expose Reminders tools using the official MCP Go SDK.
Python, a gateway and a reverse proxy are not required. HTTP is stateless and
supports current clients as well as the 2025 Streamable HTTP protocol. Both
`/mcp` and `/mcp/` work without redirects.

## Local setup

```bash
bash scripts/build.sh
./scripts/reminders auth
./scripts/reminders serve
```

The default endpoint is `http://127.0.0.1:8081/mcp`. For a client that launches
an MCP process, use `./scripts/reminders serve --transport stdio`. Server logs
go to stderr, keeping stdout reserved for MCP messages.

Use `--data-dir /path/to/private-data` on **both** `auth` and `serve` to choose
another account directory. `ICLOUD_REMINDERS_DATA_DIR` provides the same default
for all commands. The directory holds `session.json`, `ck_cache.json`, and
optionally a `credentials` file. Session and cache writes replace private files
atomically. Each CLI invocation holds an exclusive directory lock: stop a
running server before authentication, session import, or another CLI operation
on that directory. Use a separate directory for each account and one server
per directory. The server also resets cached records if the CloudKit account
owner changes.

The server only reuses or refreshes an existing web session. It never reads
Apple passwords, prompts on stdin, or initiates password/2FA authentication.
Missing or expired sessions produce an MCP tool error beginning with
`auth_required`. Stop the server, run `reminders auth` with the same directory,
then start it again. This is the iCloud web-login flow; CalDAV/IMAP
app-specific-password authentication is separate. Keep Apple passwords and
session files out of MCP arguments, Git, Docker build contexts, and server logs.

## Docker

```bash
docker compose build
# Interactive one-time login and 2FA; credentials are not baked into the image.
docker compose run --rm reminders auth
docker compose up -d
docker compose ps
```

The container runs as a non-root user. Its session and delta cache persist in
the `reminders-data` volume. Compose publishes only `127.0.0.1:8081`; services
on the same private Docker network can use `http://reminders:8080/mcp`.
`/healthz` and the Docker health check report process liveness, not whether
iCloud authentication is valid. Tools can be discovered before authentication.

To renew authentication:

```bash
docker compose stop reminders
docker compose run --rm reminders auth
docker compose up -d reminders
```

Keep the write-capable endpoint on loopback or a private network. Optional
`REMINDERS_MCP_TOKEN` requires `Authorization: Bearer <token>` on `/mcp`;
tokens are not accepted as URL parameters. Set it in the server environment
or an untracked Compose `.env` file. A fixed bearer token is a private-service
access control, not a public OAuth deployment. `/healthz` remains unauthenticated
and contains no account information. Requests with an `Origin` header are
rejected unless explicitly allowed by `--allow-origin https://trusted.example`.

## Tools

| Tool | Behavior |
| --- | --- |
| `list_reminder_lists` | Read existing lists and their exact IDs |
| `list_reminders` | Filter by `list_id`, `parent_id`, title `query`, or `include_completed`; paginate using `limit` and `offset` |
| `get_reminder` | Read one reminder, including notes and list/parent references |
| `create_reminder` | Create in an existing `list_id`, optionally with a `parent_id`, due date, notes and priority |
| `update_reminder` | Update specified nonempty fields; `priority=none` clears priority |
| `complete_reminder` | Mark complete; an already completed reminder performs no write |
| `delete_reminder` | Permanently delete; requires `confirm=true` |
| `sync_reminders` | Refresh the local delta cache, or perform a full sync with `full=true` |

Use the exact IDs returned by the tools. MCP writes reject partial IDs and
ambiguous list names. Creation requires an existing list; list creation and
deletion are not exposed. Due dates use `YYYY-MM-DD` and priorities are `none`,
`low`, `medium`, or `high`. Clearing an existing due date or notes is not
supported by this initial adapter. Reads refresh the delta cache before
returning results; pagination is ordered by ID and is not a snapshot across
calls. The default page size is 100, with a maximum of 500.

All account operations are serialized. `--request-timeout` bounds the total
tool duration, including queue wait; its default is 3 minutes. iCloud HTTP
requests also have a 30-second per-request limit and observe cancellation.
Writes are never automatically retried. A timeout or failed response can leave
a write's outcome uncertain: inspect the current reminders before retrying,
especially before creating another reminder. Logs include tool names, call
IDs, outcomes, durations and counts, never arguments, reminder contents,
credentials, remote URLs or upstream exception text.

## Verification

```bash
go test -race ./...
go vet ./...
docker compose config --quiet
docker compose build
```

Tests use a simulated CloudKit API and exercise MCP discovery, legacy HTTP
compatibility, CRUD, exact IDs, deletion confirmation, account isolation,
serialization, cancellation, failed-write cache handling and safe errors/logs.
They do not access a real Apple account. Live authentication and writes need
separate verification with a test account.

## Read-only live smoke test

After authenticating, test the real Go backend through its MCP HTTP interface:

```bash
docker compose run --rm reminders auth
docker compose --profile test run --rm --build smoke
```

The `smoke` service mounts the account volume read-only, copies only its session
into a private temporary directory, and performs a fresh full sync there. It
discovers the MCP tools, lists current lists and active reminders, and reads
one sampled reminder when available. The original session and cache are not
modified, and the test cannot call create/update/complete/delete tools. It logs
only aggregate counts; titles, IDs, notes, cookies and credentials stay private.
Allow several minutes for the first full sync. The source session must already
be valid: missing or expired authentication fails the explicitly enabled test.

An empty account is a valid result. To require at least one active reminder:

```bash
REMINDERS_LIVE_MIN_ACTIVE=1 docker compose --profile test run --rm --build smoke
```

For a local Go installation and session directory:

```bash
ICLOUD_REMINDERS_DATA_DIR=/path/to/private-data REMINDERS_LIVE_TEST=1 \
  go test -v ./internal/mcpserver -run '^TestLiveRemindersReadOnly$' -count=1 -timeout 10m
```

Normal `go test ./...` skips live testing unless `REMINDERS_LIVE_TEST=1` is set.
There is no interactive authentication inside the test. If the standalone
server is already running, stop it before the administrative `auth` command;
the smoke test itself uses a separate temporary cache and can run alongside it.

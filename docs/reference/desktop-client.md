<a id="桌面客户端-gateway"></a>

# Desktop Client Gateway

[English](desktop-client.md) | [简体中文](desktop-client.zh-CN.md)

The public core provides `amux gui serve`, exposing the existing Session API as
HTTP JSON and WebSocket for desktop clients. Independent distributions maintain
the Electron application; this module includes neither its source nor installers.

<a id="组件与连接"></a>

## Components and connections

| Component | Public source | Responsibility |
| --- | --- | --- |
| Gateway CLI | `app/cli/gui.go` | Listen, emit readiness, and adapt the existing Session client |
| HTTP/WebSocket server | `internal/gateway/` | Bearer token validation, REST, and Attach streaming |
| Protocol types | `internal/transport/pbgen/` | gRPC request, response, and event structures |

`internal/gateway` receives capabilities through a Backend interface and does not
depend on CLI wiring. `app/cli/gui.go` injects that backend. Public
`cmd/amux/main.go` is a thin entrypoint calling `app/cli.Main`; other distributions
can register extensions in their own entrypoints before calling the public app.

```text
Desktop renderer -> HTTP/WebSocket gateway -> amuxd Hub -> selected amuxlet
```

Local Sessions also use the Hub control plane. Run `amux setup local` for a new
installation or `amux system status` to check an existing Hub/Runtime. The Gateway
does not automatically bypass the Hub to connect directly to the local Runtime.

<a id="启动与认证"></a>

## Startup and authentication

```sh
amux gui serve --addr 127.0.0.1:0
```

The client's main process reads the readiness line on stdout:

```text
AMUX_GUI_READY {"addr":"127.0.0.1:<port>","token":"<per-process-token>"}
```

The OS allocates the port; the token is random by default. The client shares these
values only with its own renderer. REST requests carry `Authorization: Bearer <token>`;
Attach uses a query token. Stop the spawned Gateway process when the app exits.
Startup errors and logs go to stderr.

The default address is loopback. `--addr` can override it explicitly; desktop
distributions should pass a fixed loopback address. The Gateway permits
cross-origin requests: access control depends on its listening address and token,
not the browser Origin.

## Browser operator console

`amux console` is a separate same-origin browser surface, not a public deployment
of this desktop Gateway. It embeds its own dependency-free web assets and uses
temporary-password authentication, HttpOnly sessions, CSRF protection, exact
Host/Origin checks and backend action allowlists. The owner's private `/amux`
page opens/closes one installation-wide listener on all IPv4 interfaces at
`console.port` (7892 by default). It closes and revokes sessions after 30 minutes.
The card gives the server URL and temporary password; `console.public_url` may
override the advertised origin. HTTP is only for trusted networks; public access
should use HTTPS. Local CLI diagnostics retain one-time pairing. `/api/console/bootstrap` supplies browser capabilities;
`/api/console/<resource>` reads snapshots or accepts typed control operations;
`/api/console/events` streams authenticated canonical Attach events over SSE.
Reconnect replays the existing stream and the client deduplicates canonical
cursors/event IDs; it never replays control requests. These sessions have host
operator authority, not independent end-user roles. Credential values and raw
launch environments are excluded from management snapshots. Bot/group writes
use the operator-only Hub `ConsoleManagement` RPC and the live Bridge transition.
There is no Runtime-node management page or raw terminal endpoint.

## API

| Path | Purpose |
| --- | --- |
| `/api/health` | Health and version |
| `/api/sessions` | Sessions, including target identifiers |
| `/api/run` | Create a Session |
| `/api/kill`, `/api/restart`, `/api/interrupt` | Terminate, restart, interrupt |
| `/api/submit` | Submit text to a Session queue |
| `/api/respond-permission` | Answer a permission request |
| `/api/queue/list`, `/api/queue/clear`, `/api/queue/cancel` | Manage pending inputs |
| `/api/prune` | Delete archived Sessions on a specified target |

`/api/health` and `/api/sessions` use GET; other operations use POST JSON. Request
types in `internal/gateway/rest.go` define the fields. Sleep/resume HTTP routes
are not yet exposed.

`GET /attach?name=<session>&target_id=<target>&token=<token>` upgrades to WebSocket
and sends JSON `AttachEvent` values; Go `[]byte` fields are base64-encoded. The
stream reads events; input and control still use REST. Desktop submissions use
`source=desktop`.

A Session is identified by target and name together. Remote operations must
include `target_id`; group batch pruning by target. Every Session uses line view,
with input entering the queue through Submit.

<a id="验证与诊断"></a>

## Validation and diagnostics

Run `go test ./internal/gateway ./app/cli` in the public module. Distribution docs
own client builds, sidecars, binary bundling, and signing. A binary built from
public `cmd` does not imply that distribution extensions are included.

For startup failures, inspect Gateway stderr. For unavailable tools, run
`amux system status`. Permission responses must use the currently pending
permission ID. See the [CLI model](../current/cli-product-model.md) for Hub behavior
and the [CLI reference](cli.md) for command arguments.

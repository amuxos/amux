<a id="公共核心与独立发行版"></a>

# Public Core and Independent Distributions

[English](distribution-development.md) | [简体中文](distribution-development.zh-CN.md)

This module is the single source of the public core and builds and tests independently.
It includes Codex, Claude, Pi, public Profiles, CLI, Hub, Runtime, Task, Session,
Timeline, and integration through the official Feishu APIs.

The public repository is <https://github.com/amuxos/amux.git>, the Go module is
`github.com/amuxos/amux`, and the website is <https://amuxos.com/>.

The public core uses [Apache-2.0](../LICENSE). Commercial and proprietary
distributions are permitted; retain the applicable license and attribution
material when redistributing it. The public packager includes core notices under
`licenses/amux/`, including when building a dependent distribution. This does not
license that distribution's own code. See [License and attribution](licensing.md)
and [Third-party notices](../THIRD_PARTY_NOTICES.md).

Other distributions create independent Go modules, depend on this module, register
additional capabilities through `pkg/extensions`, and call `app/cli.Main`,
`app/hub.Main`, or `app/runtime.Main` from their thin `cmd` entrypoints.
Registration closes when the application starts; finish all registration first.
Public packages do not depend on any distribution module.

```text
Distribution main -> register extensions -> public app entrypoint -> public runtime
                            +-- Adapter / Provider / Profile / Bridge / configuration
```

Change common functionality only here. Distributions own their Providers, Profile
resources, configuration defaults, release sources, and branding. During monorepo
development they use the public source through a relative `replace`; after
independent releases they update dependency versions. Do not maintain branch
patches, source overlays, or exported copies. `cmd` remains the standard build
entrypoint; implementations and tests live in `app`.

The core owns Pi's common file layout and extension rendering. Distributions use
Profile customization's `ValidateAdditionalProvider` and `PiProviderConfig` to register
validation, connection URLs, APIs, and model compatibility settings for additional
platforms. The core still handles the default `openai-compat` path. CLI
customization's `CredentialSetExamples` can append distribution-specific credential
examples using the supplied command path.
Task permission decisions belong to `permission_policy`. Additional engines can
supply native permission arguments through `PermissionModeArgs` and remove their
legacy permission flags through `StripTaskPermissionArgs` before policy projection.
Config-only arguments stay in the execution Profile snapshot for materialization;
`LaunchProcessArgs` filters them from process argv. Restricted raw launches that
cannot materialize their permission configuration are rejected before execution.
These hooks do not add permission settings to manifest v2 content.

The `SetupCommands` hook may return fresh Cobra commands for optional local
component setup under `amux setup`. Registration must not perform installation;
only explicit command execution may download or install the component.

For local integration, use a relative `replace` in the distribution's `go.mod` or
an uncommitted `go.work`. After the public release, pin a released version of
`github.com/amuxos/amux` and remove the local replacement. `v0.0.0` is a local
placeholder, not a published version resolvable over the network. `make check`
validates the public core independently; distributions also run their own
customization and integration tests.

The interface respects Go's `internal` boundary. Facades expose only extension
contracts; Session and Timeline implementation and ownership stay inside the
core. Providers reuse the same canonical event path, without separate Session,
turn completion, or transport implementations. This interface does not yet carry
a stability guarantee.

## Coordinating changes with a distribution

This GitHub repository owns public implementation, interfaces, tests, and bilingual
documentation. Distributions pin a reviewed public commit or release. A distribution
may keep this repository as a submodule and use a relative Go `replace` for local
development, or configure an independent checkout.

For a change spanning both repositories, create a feature branch in each checkout
and validate the public core together with the distribution's actual assembly.
Submit and merge the public PR first, then update the distribution to the resulting
public commit and validate that combination before merging its dependency update.
Each repository records its own commits; staging a parent submodule path records
only its commit, not uncommitted changes inside the public checkout. CI and releases
use the recorded revision rather than following a moving branch.

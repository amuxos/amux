<!-- Code generated from app/cli's Cobra command tree; DO NOT EDIT. -->

# amux CLI Reference

[English](cli.md) | [简体中文](cli.zh-CN.md)

This reference is generated from the current `amux` Cobra command tree. It covers public commands, arguments, options, defaults, aliases, and registered examples on every supported platform. Root options are global options available to all subcommands. Platform restrictions are explicit. Hidden commands and flags are excluded from the public CLI contract.

After changing public command names, hierarchy, visibility, descriptions, arguments, flags, or defaults, run `make cli-doc`. The repository check rejects stale generated output. Product semantics and behavior boundaries are defined by the [CLI product model](../current/cli-product-model.md).

## Command index

| Command | Description |
| --- | --- |
| `amux` | Unified manager for code agents across runtimes |
| `amux agent` | Manage agent profiles |
| `amux agent credential` | Manage credential profiles |
| `amux agent credential ls` | List credential profiles |
| `amux agent credential set` | Save one environment secret in a credential profile |
| `amux agent credential setup` | Set up one credential for an identified Principal |
| `amux agent credential show` | Show credential profile metadata without secret values |
| `amux agent doctor` | Diagnose an agent profile |
| `amux agent list` | List available agent profiles |
| `amux agent pack` | Manage agent profile packs |
| `amux agent pack add` | Add a Git-backed agent pack |
| `amux agent pack autosync` | Manage background agent pack auto sync |
| `amux agent pack autosync logs` | Show background agent pack auto sync log path |
| `amux agent pack autosync start` | Start background agent pack auto sync |
| `amux agent pack autosync status` | Show background agent pack auto sync status |
| `amux agent pack autosync stop` | Stop background agent pack auto sync |
| `amux agent pack ls` | List agent packs |
| `amux agent pack remove` | Remove an agent pack |
| `amux agent pack rollback` | Roll back a pack to a recorded revision |
| `amux agent pack sync` | Sync one or all agent packs |
| `amux agent pack versions` | List recorded pack revisions |
| `amux agent pack watch` | Continuously sync an agent pack |
| `amux agent render` | Render a profile workspace projection |
| `amux agent show` | Show an agent profile |
| `amux completion` | Generate the autocompletion script for the specified shell |
| `amux completion bash` | Generate the autocompletion script for bash |
| `amux completion fish` | Generate the autocompletion script for fish |
| `amux completion powershell` | Generate the autocompletion script for powershell |
| `amux completion zsh` | Generate the autocompletion script for zsh |
| `amux console` | Open the operator web management console |
| `amux help` | Help about any command |
| `amux isolation` | Manage identified principals and Linux UID/DAC isolation |
| `amux isolation account` | Inspect target-local principal account mappings |
| `amux isolation account gc` | Preview or explicitly remove idle principal accounts |
| `amux isolation account list` | List mappings known to this runtime |
| `amux isolation alias` | Manage scoped principal aliases |
| `amux isolation alias bind` | Bind provider/scope/subject to a principal |
| `amux isolation alias list` | List scoped principal aliases |
| `amux isolation delegation` | Manage run_as delegations |
| `amux isolation delegation grant` | Grant an actor permission to run as another principal |
| `amux isolation delegation revoke` | Revoke a run_as delegation |
| `amux isolation enroll` | Create and store a CLI actor credential |
| `amux isolation global-env` | Manage environment secrets shared by identified isolated executions |
| `amux isolation global-env ls` | List shared isolated environment names without values |
| `amux isolation global-env set` | Save one installation-wide isolated environment secret |
| `amux isolation global-env unset` | Remove one installation-wide isolated environment secret |
| `amux isolation helper` | Install and manage the privileged Linux helper |
| `amux isolation helper install` | Explicitly install and start the root helper systemd unit |
| `amux isolation helper status` | Check the local helper handshake |
| `amux isolation helper uninstall` | Stop and remove the helper; preserve account data by default |
| `amux isolation principal` | Manage canonical principals |
| `amux isolation principal create` | Create a human or service principal |
| `amux isolation principal disable` | Disable a principal for new execution and authorization |
| `amux isolation principal list` | List principals |
| `amux isolation service-member` | Manage service-principal membership |
| `amux isolation service-member grant` | Allow a human principal to run as a service principal |
| `amux isolation service-member revoke` | Revoke service-principal membership and active service launches |
| `amux isolation status` | Show isolation configuration, helper readiness, and fallback state |
| `amux memory` | Manage shared Markdown memory through the Hub |
| `amux memory export` | Export one complete memory document to standard output |
| `amux memory forget` | Conditionally delete a memory document |
| `amux memory search` | Search memory using literal keywords |
| `amux memory show` | Read a bounded memory document or line range |
| `amux memory upsert` | Conditionally save a UTF-8 Markdown memory document |
| `amux schedule` | Schedule prompts or subtasks on a timer |
| `amux schedule create` | Create a scheduled prompt |
| `amux schedule create-task` | Create a scheduled subtask |
| `amux schedule disable` | Disable a scheduled prompt |
| `amux schedule enable` | Enable a scheduled prompt |
| `amux schedule list` | List scheduled prompts and subtasks |
| `amux schedule remove` | Delete a scheduled prompt |
| `amux schedule runs` | List runs for a schedule |
| `amux schedule update` | Update a scheduled prompt or subtask in place |
| `amux session` | Manage agent sessions |
| `amux session attach` | Attach to a session (/detach or Ctrl-] to detach) |
| `amux session cwd` | Show or change a session working directory |
| `amux session interrupt` | Interrupt the current turn without killing the session |
| `amux session list` | List sessions |
| `amux session prune` | Remove archived (invalid) sessions from the default target |
| `amux session queue` | Show pending input queue for a session |
| `amux session queue cancel` | Cancel one pending input |
| `amux session queue clear` | Clear pending input queue for a session |
| `amux session restart` | Restart one session or all running local user sessions |
| `amux session resume` | Resume a sleeping session on its current runtime |
| `amux session run` | Start an agent and register it as a session |
| `amux session send` | Send one message to a session without attaching |
| `amux session set-model` | Switch a session's model while retaining its conversation history |
| `amux session sleep` | Stop a session process while keeping it resumable |
| `amux session stop` | Stop a session and its agent process |
| `amux setup` | Set up this amux installation |
| `amux system` | Inspect and manage this amux installation |
| `amux system cleanup` | Preview managed data cleanup; --apply cleans and reports to administrators |
| `amux system config` | Manage amux configuration |
| `amux system config get` | Get config value |
| `amux system config path` | Print config file path |
| `amux system config set` | Set config value |
| `amux system config show` | Print config with shared environment values redacted |
| `amux system config unset` | Unset config value |
| `amux system diagnose` | Correlate a message, turn, task, or session |
| `amux system doctor` | Diagnose this installation without changing it |
| `amux system logs` | Show installation log paths |
| `amux system operation` | Inspect and cancel lifecycle operations |
| `amux system operation cancel` | Cancel a pending lifecycle operation |
| `amux system operation list` | List lifecycle operations |
| `amux system operation show` | Show a lifecycle operation |
| `amux system restart` | Restart this amux installation |
| `amux system service` | Manage the Amux Windows Service (hub + runtime) |
| `amux system service install` | Install the Amux Windows Service (Automatic start) |
| `amux system service start` | Start the Amux Windows Service |
| `amux system service status` | Show Amux Windows Service status |
| `amux system service stop` | Stop the Amux Windows Service |
| `amux system service uninstall` | Stop and remove the Amux Windows Service |
| `amux system start` | Start this amux installation |
| `amux system status` | Show installation status |
| `amux system stop` | Stop this amux installation |
| `amux system target` | Manage runtime targets |
| `amux system target list` | List runtimes connected to the hub |
| `amux system uninstall` | Uninstall amux from the current user |
| `amux task` | Run and manage subagent tasks |
| `amux task cancel` | Cancel a task |
| `amux task list` | List tasks |
| `amux task logs` | Stream a task's events until it finishes (read-only) |
| `amux task prune` | Delete terminal task history |
| `amux task respond` | Answer an escalated permission request (escalate-to-main) |
| `amux task show` | Show a task's status and result |
| `amux task submit` | Submit a subagent task |

## `amux`

Unified manager for code agents across runtimes

**Usage**

```text
amux [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux agent` | Manage agent profiles |
| `amux completion` | Generate the autocompletion script for the specified shell |
| `amux console` | Open the operator web management console |
| `amux help` | Help about any command |
| `amux isolation` | Manage identified principals and Linux UID/DAC isolation |
| `amux memory` | Manage shared Markdown memory through the Hub |
| `amux schedule` | Schedule prompts or subtasks on a timer |
| `amux session` | Manage agent sessions |
| `amux setup` | Set up this amux installation |
| `amux system` | Inspect and manage this amux installation |
| `amux task` | Run and manage subagent tasks |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for amux |
| `--json` | `bool` | `false` | output stable JSON |
| `-v, --version` | `bool` | `false` | version for amux |

## `amux agent`

Manage agent profiles

**Usage**

```text
amux agent [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux agent credential` | Manage credential profiles |
| `amux agent doctor` | Diagnose an agent profile |
| `amux agent list` | List available agent profiles |
| `amux agent pack` | Manage agent profile packs |
| `amux agent render` | Render a profile workspace projection |
| `amux agent show` | Show an agent profile |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for agent |

## `amux agent credential`

Manage credential profiles

**Usage**

```text
amux agent credential [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux agent credential ls` | List credential profiles |
| `amux agent credential set` | Save one environment secret in a credential profile |
| `amux agent credential setup` | Set up one credential for an identified Principal |
| `amux agent credential show` | Show credential profile metadata without secret values |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for credential |

## `amux agent credential ls`

List credential profiles

**Usage**

```text
amux agent credential ls [flags]
```

**Aliases:** `list`

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for ls |

## `amux agent credential set`

Save one environment secret in a credential profile

**Usage**

```text
amux agent credential set <profile> <env-name> [flags]
```

**Examples**

```bash
amux agent credential set openai-compat OPENAI_API_KEY
printf '%s\n' "$OPENAI_API_KEY" | amux agent credential set openai-compat OPENAI_API_KEY
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for set |

## `amux agent credential setup`

Set up one credential for an identified Principal

**Usage**

```text
amux agent credential setup <profile> <env-name> [flags]
```

**Examples**

```bash
amux agent credential setup openai-compat GLM_API_KEY --principal <principal-id>
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--from-legacy-host` | `bool` | `false` | copy the value from the legacy-host credential store |
| `-h, --help` | `bool` | `false` | help for setup |
| `--principal` | `string` | `""` | target Principal UUID (required) |
| `--rotate-enrollment` | `bool` | `false` | replace the local CLI enrollment with the target Principal |

## `amux agent credential show`

Show credential profile metadata without secret values

**Usage**

```text
amux agent credential show <profile> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for show |

## `amux agent doctor`

Diagnose an agent profile

**Usage**

```text
amux agent doctor [profile] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for doctor |
| `--model` | `string` | `""` | model to diagnose within the selected runtime |
| `--runtime` | `string` | `""` | runtime adapter to diagnose with the Agent Profile |
| `--smoke` | `bool` | `false` | run one real end-to-end task turn after static checks |
| `--smoke-timeout` | `int` | `120` | smoke turn timeout in seconds |

## `amux agent list`

List available agent profiles

**Usage**

```text
amux agent list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | include dev profile namespaces |
| `--dev` | `bool` | `false` | show only dev profile namespaces |
| `-h, --help` | `bool` | `false` | help for list |

## `amux agent pack`

Manage agent profile packs

**Usage**

```text
amux agent pack [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux agent pack add` | Add a Git-backed agent pack |
| `amux agent pack autosync` | Manage background agent pack auto sync |
| `amux agent pack ls` | List agent packs |
| `amux agent pack remove` | Remove an agent pack |
| `amux agent pack rollback` | Roll back a pack to a recorded revision |
| `amux agent pack sync` | Sync one or all agent packs |
| `amux agent pack versions` | List recorded pack revisions |
| `amux agent pack watch` | Continuously sync an agent pack |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for pack |

## `amux agent pack add`

Add a Git-backed agent pack

**Usage**

```text
amux agent pack add <namespace> <git-url> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--dev-of` | `string` | `""` | formal pack namespace that owns this development pack |
| `-h, --help` | `bool` | `false` | help for add |
| `--no-auto-sync` | `bool` | `false` | record the pack with auto sync disabled |
| `--ref` | `string` | `""` | Git ref to sync, such as stable, main, or a tag |
| `--sync-interval` | `string` | `30s` | default auto sync interval recorded for the pack |
| `--temporary` | `bool` | `false` | mark a --dev-of pack as temporary and disable auto sync |

## `amux agent pack autosync`

Manage background agent pack auto sync

**Usage**

```text
amux agent pack autosync [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux agent pack autosync logs` | Show background agent pack auto sync log path |
| `amux agent pack autosync start` | Start background agent pack auto sync |
| `amux agent pack autosync status` | Show background agent pack auto sync status |
| `amux agent pack autosync stop` | Stop background agent pack auto sync |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for autosync |

## `amux agent pack autosync logs`

Show background agent pack auto sync log path

**Usage**

```text
amux agent pack autosync logs [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for logs |

## `amux agent pack autosync start`

Start background agent pack auto sync

**Usage**

```text
amux agent pack autosync start [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for start |

## `amux agent pack autosync status`

Show background agent pack auto sync status

**Usage**

```text
amux agent pack autosync status [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for status |

## `amux agent pack autosync stop`

Stop background agent pack auto sync

**Usage**

```text
amux agent pack autosync stop [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for stop |

## `amux agent pack ls`

List agent packs

**Usage**

```text
amux agent pack ls [flags]
```

**Aliases:** `list`

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for ls |

## `amux agent pack remove`

Remove an agent pack

**Usage**

```text
amux agent pack remove <namespace> [flags]
```

**Aliases:** `rm`, `delete`

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for remove |
| `--keep-files` | `bool` | `false` | unregister the pack without deleting its local materialized files |

## `amux agent pack rollback`

Roll back a pack to a recorded revision

**Usage**

```text
amux agent pack rollback <namespace> <revision> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for rollback |

## `amux agent pack sync`

Sync one or all agent packs

**Usage**

```text
amux agent pack sync [namespace] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for sync |

## `amux agent pack versions`

List recorded pack revisions

**Usage**

```text
amux agent pack versions <namespace> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for versions |

## `amux agent pack watch`

Continuously sync an agent pack

**Usage**

```text
amux agent pack watch <namespace> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for watch |
| `--interval` | `string` | `30s` | sync interval |

## `amux agent render`

Render a profile workspace projection

**Usage**

```text
amux agent render <profile> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for render |
| `--model` | `string` | `""` | Model to render for the selected runtime |
| `--out` | `string` | `""` | output workspace directory |
| `--runtime` | `string` | `""` | Runtime adapter for rendering; required for external v2 profiles, optional for builtin shortcuts with amux caller defaults |

## `amux agent show`

Show an agent profile

**Usage**

```text
amux agent show <profile> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for show |

## `amux completion`

Generate the autocompletion script for the specified shell

Generate the autocompletion script for amux for the specified shell.
See each sub-command's help for details on how to use the generated script.

**Usage**

```text
amux completion [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux completion bash` | Generate the autocompletion script for bash |
| `amux completion fish` | Generate the autocompletion script for fish |
| `amux completion powershell` | Generate the autocompletion script for powershell |
| `amux completion zsh` | Generate the autocompletion script for zsh |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for completion |

## `amux completion bash`

Generate the autocompletion script for bash

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(amux completion bash)

To load completions for every new session, execute once:

#### Linux:

	amux completion bash > /etc/bash_completion.d/amux

#### macOS:

	amux completion bash > $(brew --prefix)/etc/bash_completion.d/amux

You will need to start a new shell for this setup to take effect.

**Usage**

```text
amux completion bash
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for bash |
| `--no-descriptions` | `bool` | `false` | disable completion descriptions |

## `amux completion fish`

Generate the autocompletion script for fish

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	amux completion fish | source

To load completions for every new session, execute once:

	amux completion fish > ~/.config/fish/completions/amux.fish

You will need to start a new shell for this setup to take effect.

**Usage**

```text
amux completion fish [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for fish |
| `--no-descriptions` | `bool` | `false` | disable completion descriptions |

## `amux completion powershell`

Generate the autocompletion script for powershell

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	amux completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.

**Usage**

```text
amux completion powershell [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for powershell |
| `--no-descriptions` | `bool` | `false` | disable completion descriptions |

## `amux completion zsh`

Generate the autocompletion script for zsh

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(amux completion zsh)

To load completions for every new session, execute once:

#### Linux:

	amux completion zsh > "${fpath[1]}/_amux"

#### macOS:

	amux completion zsh > $(brew --prefix)/share/zsh/site-functions/_amux

You will need to start a new shell for this setup to take effect.

**Usage**

```text
amux completion zsh [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for zsh |
| `--no-descriptions` | `bool` | `false` | disable completion descriptions |

## `amux console`

Open the operator web management console

**Usage**

```text
amux console [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--addr` | `string` | `127.0.0.1:7892` | listen IP and port (non-loopback requires temporary-password authentication) |
| `--duration` | `duration` | `30m0s` | maximum open duration; automatically closes on expiry (up to 30m) |
| `-h, --help` | `bool` | `false` | help for console |
| `--password-stdin` | `bool` | `false` | read a temporary password from stdin (required for non-loopback listening) |
| `--public-url` | `string` | `""` | browser-facing HTTP(S) origin; no credentials, query or fragment |

## `amux help`

Help about any command

Help provides help for any command in the application.
Simply type amux help [path to command] for full details.

**Usage**

```text
amux help [command] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for help |

## `amux isolation`

Manage identified principals and Linux UID/DAC isolation

**Usage**

```text
amux isolation [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation account` | Inspect target-local principal account mappings |
| `amux isolation alias` | Manage scoped principal aliases |
| `amux isolation delegation` | Manage run_as delegations |
| `amux isolation enroll` | Create and store a CLI actor credential |
| `amux isolation global-env` | Manage environment secrets shared by identified isolated executions |
| `amux isolation helper` | Install and manage the privileged Linux helper |
| `amux isolation principal` | Manage canonical principals |
| `amux isolation service-member` | Manage service-principal membership |
| `amux isolation status` | Show isolation configuration, helper readiness, and fallback state |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for isolation |

## `amux isolation account`

Inspect target-local principal account mappings

**Usage**

```text
amux isolation account [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation account gc` | Preview or explicitly remove idle principal accounts |
| `amux isolation account list` | List mappings known to this runtime |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for account |

## `amux isolation account gc`

Preview or explicitly remove idle principal accounts

**Usage**

```text
amux isolation account gc [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--confirm-principal` | `string` | `""` | exact principal UUID to remove |
| `--execute` | `bool` | `false` | perform irreversible account/HOME/workspace removal |
| `-h, --help` | `bool` | `false` | help for gc |
| `--older-than-days` | `int` | `0` | idle age threshold (default isolation.gc_idle_days) |

## `amux isolation account list`

List mappings known to this runtime

**Usage**

```text
amux isolation account list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |
| `--json` | `bool` | `false` | output JSON |

## `amux isolation alias`

Manage scoped principal aliases

**Usage**

```text
amux isolation alias [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation alias bind` | Bind provider/scope/subject to a principal |
| `amux isolation alias list` | List scoped principal aliases |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for alias |

## `amux isolation alias bind`

Bind provider/scope/subject to a principal

**Usage**

```text
amux isolation alias bind [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for bind |
| `--principal` | `string` | `""` | principal UUID (required) |
| `--provider` | `string` | `cli` | alias provider |
| `--scope` | `string` | `local` | provider-local scope |
| `--subject` | `string` | `""` | provider subject (required) |

## `amux isolation alias list`

List scoped principal aliases

**Usage**

```text
amux isolation alias list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |

## `amux isolation delegation`

Manage run_as delegations

**Usage**

```text
amux isolation delegation [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation delegation grant` | Grant an actor permission to run as another principal |
| `amux isolation delegation revoke` | Revoke a run_as delegation |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for delegation |

## `amux isolation delegation grant`

Grant an actor permission to run as another principal

**Usage**

```text
amux isolation delegation grant <actor-principal-id> <effective-principal-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for grant |

## `amux isolation delegation revoke`

Revoke a run_as delegation

**Usage**

```text
amux isolation delegation revoke <delegation-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for revoke |

## `amux isolation enroll`

Create and store a CLI actor credential

**Usage**

```text
amux isolation enroll [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for enroll |
| `--principal` | `string` | `""` | principal UUID (required) |
| `--provider` | `string` | `cli` | actor provider |
| `--rotate` | `bool` | `false` | replace an existing local enrollment credential |
| `--scope` | `string` | `local` | provider-local scope |
| `--subject` | `string` | `""` | provider subject (default current user) |

## `amux isolation global-env`

Manage environment secrets shared by identified isolated executions

**Usage**

```text
amux isolation global-env [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation global-env ls` | List shared isolated environment names without values |
| `amux isolation global-env set` | Save one installation-wide isolated environment secret |
| `amux isolation global-env unset` | Remove one installation-wide isolated environment secret |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for global-env |

## `amux isolation global-env ls`

List shared isolated environment names without values

**Usage**

```text
amux isolation global-env ls [flags]
```

**Aliases:** `list`

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for ls |

## `amux isolation global-env set`

Save one installation-wide isolated environment secret

**Usage**

```text
amux isolation global-env set <env-name> [flags]
```

**Examples**

```bash
amux isolation global-env set GLM_API_KEY
printf '%s\n' "$GLM_API_KEY" | amux isolation global-env set GLM_API_KEY
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for set |

## `amux isolation global-env unset`

Remove one installation-wide isolated environment secret

**Usage**

```text
amux isolation global-env unset <env-name> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for unset |

## `amux isolation helper`

Install and manage the privileged Linux helper

**Platforms:** `linux`

**Usage**

```text
amux isolation helper [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation helper install` | Explicitly install and start the root helper systemd unit |
| `amux isolation helper status` | Check the local helper handshake |
| `amux isolation helper uninstall` | Stop and remove the helper; preserve account data by default |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for helper |

## `amux isolation helper install`

Explicitly install and start the root helper systemd unit

**Platforms:** `linux`

**Usage**

```text
amux isolation helper install [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `runtime-dependent` | service user's AMUX_HOME |
| `-h, --help` | `bool` | `false` | help for install |
| `--mount-namespace` | `bool` | `true` | enable mount namespace hardening |
| `--pid-namespace` | `bool` | `true` | enable PID namespace hardening |
| `--principal-root` | `string` | `/var/lib/amux/principals` | principal data root |
| `--seccomp` | `bool` | `true` | enable provider seccomp hardening |
| `--service-gid` | `int` | `runtime-dependent` | amuxlet service gid |
| `--service-uid` | `int` | `runtime-dependent` | amuxlet service uid allowed to connect |
| `--socket` | `string` | `""` | absolute helper socket path (default <amux-home>/isolation-helper.sock) |
| `--uid-range-end` | `int` | `799999` | last dedicated principal uid |
| `--uid-range-start` | `int` | `700000` | first dedicated principal uid |

## `amux isolation helper status`

Check the local helper handshake

**Platforms:** `linux`

**Usage**

```text
amux isolation helper status [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for status |

## `amux isolation helper uninstall`

Stop and remove the helper; preserve account data by default

**Platforms:** `linux`

**Usage**

```text
amux isolation helper uninstall [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `runtime-dependent` | service user's AMUX_HOME |
| `--force` | `bool` | `false` | kill active isolated launches before uninstall |
| `-h, --help` | `bool` | `false` | help for uninstall |
| `--purge` | `bool` | `false` | request destructive data purge (requires separate manual review) |
| `--socket` | `string` | `""` | absolute helper socket path (default <amux-home>/isolation-helper.sock) |

## `amux isolation principal`

Manage canonical principals

**Usage**

```text
amux isolation principal [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation principal create` | Create a human or service principal |
| `amux isolation principal disable` | Disable a principal for new execution and authorization |
| `amux isolation principal list` | List principals |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for principal |

## `amux isolation principal create`

Create a human or service principal

**Usage**

```text
amux isolation principal create <display-name> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for create |
| `--json` | `bool` | `false` | output JSON |
| `--kind` | `string` | `human` | principal kind: human or service |
| `--target` | `string` | `""` | optional fixed environment target id |

## `amux isolation principal disable`

Disable a principal for new execution and authorization

**Usage**

```text
amux isolation principal disable <principal-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for disable |

## `amux isolation principal list`

List principals

**Usage**

```text
amux isolation principal list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |
| `--json` | `bool` | `false` | output JSON |

## `amux isolation service-member`

Manage service-principal membership

**Usage**

```text
amux isolation service-member [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux isolation service-member grant` | Allow a human principal to run as a service principal |
| `amux isolation service-member revoke` | Revoke service-principal membership and active service launches |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for service-member |

## `amux isolation service-member grant`

Allow a human principal to run as a service principal

**Usage**

```text
amux isolation service-member grant [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for grant |
| `--member` | `string` | `""` | member principal UUID (required) |
| `--service` | `string` | `""` | service principal UUID (required) |

## `amux isolation service-member revoke`

Revoke service-principal membership and active service launches

**Usage**

```text
amux isolation service-member revoke [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for revoke |
| `--member` | `string` | `""` | member principal UUID (required) |
| `--service` | `string` | `""` | service principal UUID (required) |

## `amux isolation status`

Show isolation configuration, helper readiness, and fallback state

**Usage**

```text
amux isolation status [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for status |
| `--json` | `bool` | `false` | output JSON |

## `amux memory`

Manage shared Markdown memory through the Hub

Search, read, conditionally update and forget Markdown memory in the scope selected by the Hub. No Agent Profile is required. Commands return JSON except export, which writes one complete document to standard output.

**Usage**

```text
amux memory [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux memory export` | Export one complete memory document to standard output |
| `amux memory forget` | Conditionally delete a memory document |
| `amux memory search` | Search memory using literal keywords |
| `amux memory show` | Read a bounded memory document or line range |
| `amux memory upsert` | Conditionally save a UTF-8 Markdown memory document |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for memory |

## `amux memory export`

Export one complete memory document to standard output

**Usage**

```text
amux memory export <path> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for export |

## `amux memory forget`

Conditionally delete a memory document

**Usage**

```text
amux memory forget <path> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--expected-revision` | `string` | `""` | Previously read SHA-256 revision, or absent if already missing (required) |
| `-h, --help` | `bool` | `false` | help for forget |

## `amux memory search`

Search memory using literal keywords

**Usage**

```text
amux memory search <keywords> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for search |
| `--limit` | `int32` | `5` | Maximum matching documents (1-20; 0 uses the server default) |
| `--path-prefix` | `string` | `""` | Restrict search to a relative memory path prefix |

## `amux memory show`

Read a bounded memory document or line range

**Usage**

```text
amux memory show <path> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--end-line` | `int32` | `0` | Last line to read, inclusive (0 reads to the response limit) |
| `-h, --help` | `bool` | `false` | help for show |
| `--offset` | `int64` | `0` | Byte offset returned as next_offset by the previous response |
| `--start-line` | `int32` | `0` | First line to read (1-based; 0 starts at the beginning) |

## `amux memory upsert`

Conditionally save a UTF-8 Markdown memory document

**Usage**

```text
amux memory upsert <path> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--expected-revision` | `string` | `""` | Previously read SHA-256 revision, or absent for creation (required) |
| `--file` | `string` | `-` | Read document content from this file (- reads standard input) |
| `-h, --help` | `bool` | `false` | help for upsert |

## `amux schedule`

Schedule prompts or subtasks on a timer

Manage scheduled prompts and scheduled subtasks. At the scheduled time
amuxd either injects a prompt into a target session (the same as typing it) or
submits an ephemeral subtask from a saved task spec. Schedules live in the hub
(amuxd), so a hub address must be configured.

Time spec forms:
  every <duration>   e.g. "every 30m", "every 2h" (minimum 1m)
  daily <HH:MM>      e.g. "daily 09:00" (server local time)
  at <when>          e.g. "at 2026-06-01 09:00" (one-shot)
  cron <expr>        e.g. "cron 0 9 * * 1-5" (standard 5-field)

**Usage**

```text
amux schedule [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux schedule create` | Create a scheduled prompt |
| `amux schedule create-task` | Create a scheduled subtask |
| `amux schedule disable` | Disable a scheduled prompt |
| `amux schedule enable` | Enable a scheduled prompt |
| `amux schedule list` | List scheduled prompts and subtasks |
| `amux schedule remove` | Delete a scheduled prompt |
| `amux schedule runs` | List runs for a schedule |
| `amux schedule update` | Update a scheduled prompt or subtask in place |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for schedule |

## `amux schedule create`

Create a scheduled prompt

**Usage**

```text
amux schedule create <session-ref> <spec> <prompt> [flags]
```

**Examples**

```bash
amux schedule create local/c1 "every 30m" "/daily-report run"
  amux schedule create local/c1 "at 2026-06-01 09:00" "good morning"
  amux schedule create local/c1 "cron 0 9 * * 1-5" "standup reminder"
  amux schedule create local/c1 "every 1h" "/daily-report run" --run-mode new-session-per-run
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for create |
| `--name` | `string` | `""` | optional label for the schedule |
| `--run-mode` | `string` | `reuse-session` | execution mode: reuse-session or new-session-per-run |

## `amux schedule create-task`

Create a scheduled subtask

**Usage**

```text
amux schedule create-task <spec> [flags]
```

**Examples**

```bash
amux schedule create-task "every 1h" --agent team/reviewer --task "review recent changes"
  amux schedule create-task "daily 09:00" --agent builtin/codex --task "run smoke tests" --workspace-policy git-worktree --local-dir /path/to/repo
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--agent` | `string` | `""` | agent profile ref or legacy agent type to run |
| `--arg` | `stringArray` | `[]` | native agent argument (repeatable) |
| `--credential-profile` | `string` | `""` | credential profile name (control-plane resolved) |
| `-h, --help` | `bool` | `false` | help for create-task |
| `--isolation` | `string` | `""` | isolation: process \| private-home (default process) |
| `--local-dir` | `string` | `""` | target-local working directory override |
| `--model` | `string` | `""` | Per-run model override for the scheduled execution; pass the current session model by default, otherwise the Profile or Agent default model |
| `--name` | `string` | `""` | optional label for the schedule |
| `--permission-policy` | `string` | `""` | Task and native permission policy: default or policy-allow (default), or deny-all; scheduled Tasks cannot use escalate-to-main; overrides native permission flags |
| `--reasoning-effort` | `string` | `""` | Per-run reasoning effort override for the scheduled execution; pass the current session effort by default, otherwise the selected model's declared default |
| `--repo-cache-key` | `string` | `""` | workspace repo cache key |
| `--repo-commit` | `string` | `""` | workspace repo commit/ref |
| `--repo-kind` | `string` | `""` | workspace repo kind (for example: git) |
| `--repo-remote` | `string` | `""` | workspace repo remote URL or stable id |
| `--repo-subdir` | `string` | `""` | workspace repo subdirectory |
| `--run-as` | `string` | `""` | run as an enrolled principal alias (resolved and frozen when the schedule is saved) |
| `--runtime` | `string` | `""` | Execution runtime adapter; omitted uses the builtin shortcut preset or the distribution default for other experts |
| `--task` | `string` | `""` | task prompt to run on each schedule fire |
| `--timeout` | `int` | `0` | task timeout in seconds (0 = runtime default) |
| `--trusted-mode-ack` | `bool` | `true` | legacy no-op: long-lived secrets inject under weak isolation by default (v0.38) |
| `--workspace-policy` | `string` | `temp-dir` | workspace policy: temp-dir \| git-worktree |

## `amux schedule disable`

Disable a scheduled prompt

**Usage**

```text
amux schedule disable <id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for disable |

## `amux schedule enable`

Enable a scheduled prompt

**Usage**

```text
amux schedule enable <id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for enable |

## `amux schedule list`

List scheduled prompts and subtasks

**Usage**

```text
amux schedule list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |

## `amux schedule remove`

Delete a scheduled prompt

**Usage**

```text
amux schedule remove <id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for remove |

## `amux schedule runs`

List runs for a schedule

**Usage**

```text
amux schedule runs <id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for runs |

## `amux schedule update`

Update a scheduled prompt or subtask in place

**Usage**

```text
amux schedule update <id> [flags]
```

**Examples**

```bash
amux schedule update 8 --name nightly-report
  amux schedule update 8 --spec "daily 09:00"
  amux schedule update 8 --agent builtin/codex --task "run smoke tests"
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--agent` | `string` | `""` | agent profile ref or legacy agent type to run |
| `--arg` | `stringArray` | `[]` | native agent argument (repeatable) |
| `--credential-profile` | `string` | `""` | credential profile name (control-plane resolved) |
| `-h, --help` | `bool` | `false` | help for update |
| `--isolation` | `string` | `""` | isolation: process \| private-home (default process) |
| `--local-dir` | `string` | `""` | target-local working directory override |
| `--model` | `string` | `""` | Per-run model override for the scheduled execution; pass the current session model by default, otherwise the Profile or Agent default model |
| `--name` | `string` | `""` | new label for the schedule |
| `--permission-policy` | `string` | `""` | Task and native permission policy: default or policy-allow (default), or deny-all; scheduled Tasks cannot use escalate-to-main; overrides native permission flags |
| `--reasoning-effort` | `string` | `""` | Per-run reasoning effort override for the scheduled execution; pass the current session effort by default, otherwise the selected model's declared default |
| `--repo-cache-key` | `string` | `""` | workspace repo cache key |
| `--repo-commit` | `string` | `""` | workspace repo commit/ref |
| `--repo-kind` | `string` | `""` | workspace repo kind (for example: git) |
| `--repo-remote` | `string` | `""` | workspace repo remote URL or stable id |
| `--repo-subdir` | `string` | `""` | workspace repo subdirectory |
| `--run-as` | `string` | `""` | run as an enrolled principal alias (resolved and frozen when the schedule is saved) |
| `--runtime` | `string` | `""` | Execution runtime adapter; omitted uses the builtin shortcut preset or the distribution default for other experts |
| `--spec` | `string` | `""` | new time rule (every <duration>, daily <HH:MM>, at <when>, or cron <expr>); recomputes next run |
| `--task` | `string` | `""` | task prompt to run on each schedule fire |
| `--timeout` | `int` | `0` | task timeout in seconds (0 = runtime default) |
| `--trusted-mode-ack` | `bool` | `true` | legacy no-op: long-lived secrets inject under weak isolation by default (v0.38) |
| `--workspace-policy` | `string` | `temp-dir` | workspace policy: temp-dir \| git-worktree |

## `amux session`

Manage agent sessions

**Usage**

```text
amux session [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux session attach` | Attach to a session (/detach or Ctrl-] to detach) |
| `amux session cwd` | Show or change a session working directory |
| `amux session interrupt` | Interrupt the current turn without killing the session |
| `amux session list` | List sessions |
| `amux session prune` | Remove archived (invalid) sessions from the default target |
| `amux session queue` | Show pending input queue for a session |
| `amux session restart` | Restart one session or all running local user sessions |
| `amux session resume` | Resume a sleeping session on its current runtime |
| `amux session run` | Start an agent and register it as a session |
| `amux session send` | Send one message to a session without attaching |
| `amux session set-model` | Switch a session's model while retaining its conversation history |
| `amux session sleep` | Stop a session process while keeping it resumable |
| `amux session stop` | Stop a session and its agent process |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for session |

## `amux session attach`

Attach to a session (/detach or Ctrl-] to detach)

**Usage**

```text
amux session attach <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--debug` | `bool` | `false` | print raw stdin bytes to stderr |
| `-h, --help` | `bool` | `false` | help for attach |

## `amux session cwd`

Show or change a session working directory

**Usage**

```text
amux session cwd <session-ref> [path] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--after-turn` | `bool` | `false` | queue the cwd switch after the active turn and return immediately |
| `-h, --help` | `bool` | `false` | help for cwd |
| `--interrupt` | `bool` | `false` | interrupt the active turn, then change cwd |
| `--no-wait` | `bool` | `false` | fail instead of waiting for the active turn to finish |

## `amux session interrupt`

Interrupt the current turn without killing the session

**Usage**

```text
amux session interrupt <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for interrupt |

## `amux session list`

List sessions

**Usage**

```text
amux session list [target] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | show all sessions, including task child sessions |
| `-h, --help` | `bool` | `false` | help for list |
| `--include-tasks` | `bool` | `false` | include task child sessions |

## `amux session prune`

Remove archived (invalid) sessions from the default target

**Usage**

```text
amux session prune [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for prune |

## `amux session queue`

Show pending input queue for a session

**Usage**

```text
amux session queue <session-ref> [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux session queue cancel` | Cancel one pending input |
| `amux session queue clear` | Clear pending input queue for a session |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for queue |

## `amux session queue cancel`

Cancel one pending input

**Usage**

```text
amux session queue cancel <session-ref> <queue-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for cancel |

## `amux session queue clear`

Clear pending input queue for a session

**Usage**

```text
amux session queue clear <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for clear |

## `amux session restart`

Restart one session or all running local user sessions

**Usage**

```text
amux session restart <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for restart |
| `--refresh-profile` | `bool` | `false` | refresh the session from the current Agent Profile revision and start a new provider context |
| `--running` | `bool` | `false` | restart all running local user sessions |

## `amux session resume`

Resume a sleeping session on its current runtime

**Usage**

```text
amux session resume <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for resume |

## `amux session run`

Start an agent and register it as a session

**Usage**

```text
amux session run <agent-profile|agent-type> [-- <agent-args>...] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-e, --env` | `stringArray` | `[]` | set env var KEY=VAL (repeatable) |
| `-h, --help` | `bool` | `false` | help for run |
| `--model` | `string` | `""` | override the Agent Profile model for this session |
| `-n, --name` | `string` | `""` | session name (required) |
| `--on` | `string` | `""` | target to run on (default: local) |
| `--ref` | `string` | `""` | git ref to checkout after materializing --repo |
| `--repo` | `string` | `""` | remote Git repository URL to materialize at ./repo |
| `--run-as` | `string` | `""` | run as an enrolled principal alias (resolved by amuxd) |
| `--runtime` | `string` | `""` | runtime adapter to execute the Agent Profile |

## `amux session send`

Send one message to a session without attaching

**Usage**

```text
amux session send <session-ref> <text> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for send |
| `--interrupt` | `bool` | `false` | interrupt the current turn and run this message ahead of queued inputs |
| `--steer` | `bool` | `false` | inject this message into the running turn without interrupting it; falls back to the queue |

## `amux session set-model`

Switch a session's model while retaining its conversation history

Switch the model a session runs on while retaining its amux session
identity, workspace, and conversation history.

The model may be a native model name or a provider-backed model ref declared
by the session's Agent Profile (for example custom/example-model); the bare
upstream model name (for example example-model) resolves to the matching
provider-backed option when it is unambiguous. Switching to or across a
provider-backed model starts a fresh provider conversation and carries the
history over through an amux conversation handoff.

When the session is idle and the runtime supports in-place reconfiguration
(Codex-compatible app-server with an unchanged Agent Profile and a native target model), the
model switches without restarting the agent process. Otherwise the session
restarts in place and the conversation carries over through the provider's
native resume, or through an amux conversation handoff when the runtime
cannot resume.

Sessions owned by a task or agent instance do not support set-model.

**Usage**

```text
amux session set-model <session-ref> <model> [flags]
```

**Examples**

```bash
amux session set-model mysession gpt-5.1-codex-mini
  amux session set-model mysession custom/example-model
  amux session set-model remote/mysession claude-sonnet-4-5
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for set-model |

## `amux session sleep`

Stop a session process while keeping it resumable

**Usage**

```text
amux session sleep <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for sleep |

## `amux session stop`

Stop a session and its agent process

**Usage**

```text
amux session stop <session-ref> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for stop |

## `amux setup`

Set up this amux installation

**Usage**

```text
amux setup [local|hub|remote|feishu|feishu-local] [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--agent` | `string` | `builtin/codex` | agent profile for feishu-local |
| `-h, --help` | `bool` | `false` | help for setup |
| `--hub` | `string` | `""` | hub address for remote profile |
| `--no-start` | `bool` | `false` | write config only; do not start runtime or hub |
| `--smoke` | `bool` | `false` | verify the default profile with one real task turn after setup |
| `--smoke-timeout` | `int` | `120` | setup smoke turn timeout in seconds |

## `amux system`

Inspect and manage this amux installation

**Usage**

```text
amux system [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux system cleanup` | Preview managed data cleanup; --apply cleans and reports to administrators |
| `amux system config` | Manage amux configuration |
| `amux system diagnose` | Correlate a message, turn, task, or session |
| `amux system doctor` | Diagnose this installation without changing it |
| `amux system logs` | Show installation log paths |
| `amux system operation` | Inspect and cancel lifecycle operations |
| `amux system restart` | Restart this amux installation |
| `amux system service` | Manage the Amux Windows Service (hub + runtime) |
| `amux system start` | Start this amux installation |
| `amux system status` | Show installation status |
| `amux system stop` | Stop this amux installation |
| `amux system target` | Manage runtime targets |
| `amux system uninstall` | Uninstall amux from the current user |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for system |

## `amux system cleanup`

Preview managed data cleanup; --apply cleans and reports to administrators

**Usage**

```text
amux system cleanup [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--apply` | `bool` | `false` | Apply cleanup and notify configured administrators |
| `--dry-run` | `bool` | `false` | Preview without changing data (the default) |
| `-h, --help` | `bool` | `false` | help for cleanup |
| `--target` | `string` | `""` | Runtime ID or name; empty selects all connected runtimes |

## `amux system config`

Manage amux configuration

**Usage**

```text
amux system config [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux system config get` | Get config value |
| `amux system config path` | Print config file path |
| `amux system config set` | Set config value |
| `amux system config show` | Print config with shared environment values redacted |
| `amux system config unset` | Unset config value |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for config |

## `amux system config get`

Get config value

**Usage**

```text
amux system config get <key> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for get |

## `amux system config path`

Print config file path

**Usage**

```text
amux system config path [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for path |

## `amux system config set`

Set config value

**Usage**

```text
amux system config set <key> <value> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for set |

## `amux system config show`

Print config with shared environment values redacted

**Usage**

```text
amux system config show [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for show |

## `amux system config unset`

Unset config value

**Usage**

```text
amux system config unset <key> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for unset |

## `amux system diagnose`

Correlate a message, turn, task, or session

**Usage**

```text
amux system diagnose [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for diagnose |
| `--message-id` | `string` | `""` | Feishu message ID |
| `--session-id` | `string` | `""` | amux session ID |
| `--task-id` | `string` | `""` | distributed task ID |
| `--turn-id` | `string` | `""` | amux turn ID |

## `amux system doctor`

Diagnose this installation without changing it

**Usage**

```text
amux system doctor [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for doctor |

## `amux system logs`

Show installation log paths

**Usage**

```text
amux system logs [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for logs |

## `amux system operation`

Inspect and cancel lifecycle operations

**Usage**

```text
amux system operation [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux system operation cancel` | Cancel a pending lifecycle operation |
| `amux system operation list` | List lifecycle operations |
| `amux system operation show` | Show a lifecycle operation |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for operation |

## `amux system operation cancel`

Cancel a pending lifecycle operation

**Usage**

```text
amux system operation cancel <operation-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for cancel |

## `amux system operation list`

List lifecycle operations

**Usage**

```text
amux system operation list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |

## `amux system operation show`

Show a lifecycle operation

**Usage**

```text
amux system operation show <operation-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for show |

## `amux system restart`

Restart this amux installation

**Usage**

```text
amux system restart [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | continue even when active local work would be disrupted |
| `-h, --help` | `bool` | `false` | help for restart |
| `--plan` | `bool` | `false` | preview the lifecycle operation without changing anything |

## `amux system service`

Manage the Amux Windows Service (hub + runtime)

**Usage**

```text
amux system service [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux system service install` | Install the Amux Windows Service (Automatic start) |
| `amux system service start` | Start the Amux Windows Service |
| `amux system service status` | Show Amux Windows Service status |
| `amux system service stop` | Stop the Amux Windows Service |
| `amux system service uninstall` | Stop and remove the Amux Windows Service |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for service |

## `amux system service install`

Install the Amux Windows Service (Automatic start)

**Usage**

```text
amux system service install [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `""` | amux data directory (default: current AMUX_HOME or %USERPROFILE%\.amux) |
| `--bin-dir` | `string` | `""` | directory containing amux.exe / amuxd.exe / amuxlet.exe (default: directory of this amux) |
| `-h, --help` | `bool` | `false` | help for install |

## `amux system service start`

Start the Amux Windows Service

**Usage**

```text
amux system service start [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for start |

## `amux system service status`

Show Amux Windows Service status

**Usage**

```text
amux system service status [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for status |

## `amux system service stop`

Stop the Amux Windows Service

**Usage**

```text
amux system service stop [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for stop |

## `amux system service uninstall`

Stop and remove the Amux Windows Service

**Usage**

```text
amux system service uninstall [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for uninstall |

## `amux system start`

Start this amux installation

**Usage**

```text
amux system start [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for start |
| `--plan` | `bool` | `false` | preview the lifecycle operation without changing anything |

## `amux system status`

Show installation status

**Usage**

```text
amux system status [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for status |

## `amux system stop`

Stop this amux installation

**Usage**

```text
amux system stop [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | continue even when active local work would be disrupted |
| `-h, --help` | `bool` | `false` | help for stop |
| `--plan` | `bool` | `false` | preview the lifecycle operation without changing anything |

## `amux system target`

Manage runtime targets

**Usage**

```text
amux system target [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux system target list` | List runtimes connected to the hub |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for target |

## `amux system target list`

List runtimes connected to the hub

**Usage**

```text
amux system target list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |

## `amux system uninstall`

Uninstall amux from the current user

**Usage**

```text
amux system uninstall [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--bin-dir` | `string` | `""` | installed binary directory (default: ~/.amux/bin) |
| `-h, --help` | `bool` | `false` | help for uninstall |
| `--purge` | `bool` | `false` | remove ~/.amux data, config, logs, and secrets |
| `--yes` | `bool` | `false` | confirm uninstall |

## `amux task`

Run and manage subagent tasks

Submit and manage subagent tasks. A task runs an agent in an isolated per-run sandbox with a credential profile and permission policy, and returns a structured result. The CLI submits tasks to the hub, which schedules them onto a registered runtime.

**Usage**

```text
amux task [flags]
```

**Subcommands**

| Command | Description |
| --- | --- |
| `amux task cancel` | Cancel a task |
| `amux task list` | List tasks |
| `amux task logs` | Stream a task's events until it finishes (read-only) |
| `amux task prune` | Delete terminal task history |
| `amux task respond` | Answer an escalated permission request (escalate-to-main) |
| `amux task show` | Show a task's status and result |
| `amux task submit` | Submit a subagent task |

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for task |

## `amux task cancel`

Cancel a task

**Usage**

```text
amux task cancel <task-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for cancel |

## `amux task list`

List tasks

**Usage**

```text
amux task list [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for list |

## `amux task logs`

Stream a task's events until it finishes (read-only)

**Usage**

```text
amux task logs <task-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for logs |

## `amux task prune`

Delete terminal task history

**Usage**

```text
amux task prune [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--dry-run` | `bool` | `false` | show what would be deleted without deleting anything |
| `-h, --help` | `bool` | `false` | help for prune |
| `--older-than` | `string` | `""` | only prune terminal tasks older than duration (for example 24h or 7d) |
| `--status` | `string` | `""` | comma-separated terminal statuses to prune (succeeded,failed,cancelled,timed_out) |

## `amux task respond`

Answer an escalated permission request (escalate-to-main)

**Usage**

```text
amux task respond <task-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--allow` | `bool` | `false` | approve the request |
| `--deny` | `bool` | `false` | reject the request |
| `-h, --help` | `bool` | `false` | help for respond |
| `--option` | `string` | `""` | explicit provider option id (optional) |
| `--permission` | `string` | `""` | permission id to answer (from the escalation event) |
| `--reason` | `string` | `""` | reason recorded in diagnostics |

## `amux task show`

Show a task's status and result

**Usage**

```text
amux task show <task-id> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help for show |

## `amux task submit`

Submit a subagent task

**Usage**

```text
amux task submit <task prompt> [flags]
```

**Options**

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `--agent` | `string` | `builtin/codex` | agent profile ref or legacy agent type to run |
| `--arg` | `stringArray` | `[]` | extra agent CLI arg (repeatable) |
| `--commit` | `string` | `""` | git commit/ref to base a git-worktree on (default HEAD) |
| `--credential-profile` | `string` | `""` | credential profile name (control-plane resolved) |
| `-h, --help` | `bool` | `false` | help for submit |
| `--isolation` | `string` | `""` | isolation: process \| private-home (default process) |
| `--model` | `string` | `""` | Execution model ref; omitted uses the parent session's current model and reasoning effort when it runs on the session's engine, otherwise the inherited launch preset model or the selected engine default |
| `--permission` | `string` | `""` | Task and native permission policy: policy-allow (default), escalate-to-main, or deny-all; overrides native permission flags; persistent instances retain this policy |
| `--profile` | `string` | `""` | agent profile ref to resolve before scheduling (overrides --agent) |
| `--repo-dir` | `string` | `""` | repo cache dir for git-worktree workspace |
| `--run-as` | `string` | `""` | run as an enrolled principal alias (resolved by amuxd) |
| `--runtime` | `string` | `""` | Execution runtime adapter; omitted uses the builtin shortcut preset or the distribution default for other experts |
| `--timeout` | `int` | `0` | task timeout in seconds (0 = runtime default) |
| `--trusted-mode-ack` | `bool` | `true` | legacy no-op: long-lived secrets inject under weak isolation by default (v0.38) |
| `--wait` | `bool` | `false` | block until the task finishes and print the result |
| `--workspace` | `string` | `""` | workspace policy: temp-dir \| git-worktree (default temp-dir) |

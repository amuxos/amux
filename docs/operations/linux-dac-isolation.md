<a id="linux-uiddac-隔离运维"></a>

# Linux UID/DAC Isolation Operations

[English](linux-dac-isolation.md) | [简体中文](linux-dac-isolation.zh-CN.md)

This feature requires Linux, systemd, a root helper, and a dedicated UID range
that does not conflict with existing accounts. The helper runs different
Principals' Agent processes under different UIDs; tasks belonging to one Principal
share that UID and its persistent workspace. See the [Task model](../distributed-task-model.md).
A normal installation does not install the root helper or enable isolation automatically.

<a id="安装与配置"></a>

## Installation and configuration

Build matching versions of `amux`, `amuxlet`, and `amux-isolation-helper` and install
them in one directory. Run `amux isolation helper install` as the actual service
user to obtain sudo commands for review and execution. This example uses service
UID/GID 1001. Check the entire UID range for conflicts, then adapt the arguments:

```sh
sudo amux isolation helper install \
  --service-uid 1001 --service-gid 1001 \
  --amux-home /home/amux/.amux \
  --socket /home/amux/.amux/isolation-helper.sock \
  --uid-range-start 700000 --uid-range-end 799999 \
  --principal-root /var/lib/amux/principals
```

This installs the helper, matching materializer, and systemd unit. The service
user's `config.toml` must use the same socket, UID range, and Principal root:

```toml
[isolation]
enabled = true
backend = "auto"
uid_range_start = 700000
uid_range_end = 799999
helper_socket = "/home/amux/.amux/isolation-helper.sock"
mapping_db = "/home/amux/.amux/isolation-mappings.db"
principal_root = "/var/lib/amux/principals"
gc_idle_days = 0
```

Use absolute paths. After `amux system restart`, check:

```sh
amux isolation helper status
amux isolation status
amux system target list
```

Confirm helper readiness, the actual backend, and target capabilities.
Namespace/seccomp hardening is configured when installing the helper; restarting
the normal system does not reapply helper installation parameters.

<a id="身份与凭据"></a>

## Identity and credentials

```sh
amux isolation principal create "Alice" --kind human
amux isolation enroll --provider cli --scope local --subject alice --principal <principal-id>
amux agent credential set openai-compat OPENAI_API_KEY
```

With isolation enabled, new requests must carry an identifiable identity. The CLI
stores enrollment tokens securely; running agents use injected actor credentials.
Do not put tokens in command arguments, docs, or logs. Identified callers' credentials
are stored per Principal, with no fallback to legacy-host secrets when the Hub is
unreachable. Shared service identities require explicit authorization through
aliases, service membership, or delegation.

New Feishu Sessions also select a process environment from their conversation
mode: ordinary topics and singleton Sessions use the host environment
(passthrough), while isolated topics use linux-dac. Private chats and groups
without their own settings inherit the Bot default. Enabling host isolation does
not isolate every Feishu Session. Existing Sessions retain their environment;
start a new Session after changing the mode. CLI, Task and other entrypoints
continue to follow their existing execution policies.

<a id="飞书连续会话的共享身份"></a>

### Shared identities for Feishu singleton Sessions

With Linux isolation enabled, Bot singleton and group singleton Sessions use a
shared service Principal. Its alias is `service:<bot-profile>`, scoped to that
Bot's bridge ID. This is the Feishu Bot profile, such as `default`, rather than an
Agent Profile such as `builtin/codex`. Independent topics use personal Principals;
successful topic execution does not provision the shared singleton identity.

On the Hub host, use `amux bridge feishu bots` and `amux isolation alias list` to
identify the Bot scope and the authorized sender's personal Principal, then
create and bind the shared identity:

```sh
amux isolation principal create "Bot shared sessions" --kind service
# Use the service Principal ID returned by the preceding command.
amux isolation alias bind --provider feishu --scope <bridge-id> \
  --subject service:<bot-profile> --principal <service-principal-id>
amux isolation service-member grant --service <service-principal-id> \
  --member <sender-principal-id>
```

If the sender has no personal identity, create a human Principal and bind an alias
using the same bridge scope and that sender's open ID for this Bot. Grant service
membership only to explicitly authorized senders; the group's `shared` access
mode does not grant execution authority. Identity changes are saved in the Hub's
identity database and take effect immediately. The service identity uses its own
Principal credentials; supply missing model credentials as indicated by launch
errors.

`singleton requires service principal alias ... and active sender membership`
means an alias or membership is missing. Keep isolation enabled, provision the
identity, and send a new group input. After switching a test host to Linux
isolation, validate both creation and continuation for independent topics and
singleton Sessions.

<a id="升级撤销和清理"></a>

## Upgrades, revocation, and cleanup

Run the same helper install command again to upgrade. Active launches prevent
replacement; wait for them to finish and retry. Normal `amux update` does not
replace this step. `amux isolation principal disable <principal-id>` prevents
further execution and revokes active executions for that identity; persistent
accounts and working directories remain. Start with read-only inventory:

```sh
amux isolation account list
amux isolation account gc --older-than-days 90
```

Actual GC deletion requires `--execute` and an exact `--confirm-principal`;
see the [CLI reference](../reference/cli.md). Do not edit the mapping DB directly
or reuse isolated UIDs.

Helper uninstall preserves accounts and data by default and refuses while
active launches exist:

```sh
sudo amux isolation helper uninstall --amux-home /home/amux/.amux \
  --socket /home/amux/.amux/isolation-helper.sock
```

`--force` terminates active executions. Set `isolation.enabled=false` and restart
the system to use the ordinary backend for new executions. Saved `linux-dac`
objects are not rewritten to passthrough; resuming them still requires restoring the helper.

<a id="诊断"></a>

## Diagnostics

```sh
systemctl status amux-isolation-helper.service
journalctl -u amux-isolation-helper.service -n 200 --no-pager
amux isolation status
amux isolation account list
```

- Helper unavailable: check the socket, service UID, protocol version, and configuration. Restarting objects pinned to `linux-dac` never silently reduces their isolation.
- Hardening degraded: check host namespace/seccomp support and the actual UID/DAC backend.
- Agent cannot execute: check Principal UID access to every directory in the binary path and symlink target. Install shared programs in suitable system paths instead of relaxing permissions on the entire service account HOME.
- Credentials unavailable: verify the current Principal and variables declared by the selected Profile; repair through credential commands.
- Helper crash: inspect active processes and `isolation_lost`, restore the helper, and explicitly restart affected Sessions.

The offline code check is `scripts/check.sh`. On a dedicated Linux test machine,
you may additionally run `sudo -E scripts/test-linux-dac.sh`. It creates temporary
high UIDs and accounts and must not be run as an ordinary local check.

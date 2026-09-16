<a id="02--分布式一个-control--多个-runtime"></a>

# 02 · Distributed: One Control Service, Multiple Runtimes

[English](README.md) | [简体中文](README.zh-CN.md)

Scale task scheduling across machines: **one amux control service** schedules and
routes work while **multiple amuxlet Runtimes** connect back to the Hub and execute
Sessions/Tasks. Once the CLI has `hub.addr`, `amux session run` and `amux task`
route through the Hub to an appropriate Runtime.

```
   amux CLI ──► amuxd (Hub)  ◄── reverse tunnel ── amuxlet (Runtime A)
                   │                         ◄────────────────── amuxlet (Runtime B)
                   └─ placement + routing + task index
```

<a id="a-control-主机调度中心"></a>

## A. Control host (scheduler)

```bash
./hub-host.sh
# Equivalent to:
#   amux setup hub             # Write a local hub.addr and start the local amux system.
#   amux system target list    # Inspect registered targets.
```

> The Hub listens on local `127.0.0.1:7100` by default. For remote connections,
> explicitly set `hub.addr` to `0.0.0.0:<port>` or an interface address and **set
> `hub.token`**. Also consider `hub.allowed_targets` to restrict which runtime.id
> values may register (see `config.hub.toml`). **Task-path Agent Profile resolution
> happens on the Hub host**: register Packs there using
> `amux agent pack add <namespace> <git-url-or-local-git-repo>`.
> `amux task submit --profile <namespace/profile>` is resolved to a base agent and routed by amuxd.

<a id="b-每台-runtime-主机执行机"></a>

## B. Each Runtime host (executor)

```bash
HUB=hub-host.example.com:7070 ./runtime-host.sh
# Equivalent to:
#   amux setup remote --hub hub-host.example.com:7070  # Write hub.addr and start the local system.
#   # Set an identifiable target ID, capacity, and labels:
#   amux system config set runtime.id        gpu-box-1
#   amux system config set runtime.max_concurrency 4
#   amux system config set runtime.labels.gpu true
#   amux system restart
```

<a id="c-从任意配置了-hub-的机器提交"></a>

## C. Submit from any machine configured with the Hub

```bash
amux system target list                             # Check target availability.
amux session run builtin/codex -n work --on gpu-box-1 # Select a target for the Session.
amux task submit "Run the tests"                    # Use the default Profile.
amux task submit "Review the changes" --profile team/reviewer
```

<a id="配置文件参考透明对照"></a>

## Configuration file reference

- [`config.hub.toml`](config.hub.toml): key sections of the Hub host's `~/.amux/config.toml`.
- [`config.runtime.toml`](config.runtime.toml): key sections for each Runtime host.

> `amux setup hub` and `amux setup remote` write these settings automatically. Use the examples as a reference when editing configuration manually.

<a id="单机演练隔离-home不污染默认-amux"></a>

## Single-machine demo (isolated HOME)

You can exercise the complete Hub path without a second machine: Hub, Runtime,
and submission with task-path Profile resolution through the Hub.

```bash
./local-hub-demo.sh
```

The script starts the Hub and Runtime with a temporary HOME and random ports,
creates a temporary Agent Pack, submits a `--profile demo/reviewer` task, verifies
that the Hub resolves the base type and routes execution, then cleans up.
Install and authenticate `codex` locally before running it.

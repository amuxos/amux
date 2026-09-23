# Run Agents on another machine

[English](remote-runtime.md) | [简体中文](remote-runtime.zh-CN.md)

This is an operator procedure. Feishu users keep using the same bot while the Hub
routes work to a selected Runtime. Install amux and authenticate the native Agent
on the worker; laptop logins and files are not copied there automatically.

## Establish a Hub connection

The worker connects outward to the Hub. One way to reach a Hub that listens only
on loopback is an SSH forward from the worker, kept running in a separate terminal:

```sh
ssh -N -L 17100:127.0.0.1:7100 operator@hub-host
```

Replace the SSH destination and port with the actual Hub host configuration.
Then, on the worker:

```sh
amux system config set runtime.id worker-a
amux setup remote --hub 127.0.0.1:17100
amux system status
```

Choose a unique stable Runtime ID. This changes the worker's configuration and
service topology; it is intended for a dedicated worker installation. The SSH
connection must stay available. If Hub token authentication or target allowlists
are configured, provision the matching `hub.token` and allow this Runtime ID.
Direct non-loopback Hub listeners require a token; use a trusted protected network.
Full settings are in the [configuration reference](../reference/configuration.md).

## Confirm registration and route a Session

On a CLI connected to the Hub:

```sh
amux system target list
amux session run builtin/codex --name remote-demo --on worker-a
amux session attach worker-a/remote-demo
```

The target list must contain the worker. The Agent executable, credentials and
workspace paths must exist on that worker. The `local` alias follows the CLI's
configured local Runtime; it does not mean any worker whose name happens to be
“local”. In Feishu, select an available Runtime in supported assistant/group
launch settings for new work. This does not move an existing conversation.

Persistent Agent Instances return to their recorded Runtime. Distributed routing
does not imply automatic migration or cross-machine high availability.

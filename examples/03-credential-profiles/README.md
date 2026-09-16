# Credential Profiles

[English](README.md) | [简体中文](README.zh-CN.md)

A Credential Profile references the identity needed to launch a task. The Agent
Profile's `envs`/`env_refs` declare which variables it needs. Do not put secrets in
task Prompts or Packs. See the [Pack format](../../docs/reference/agent-profile-format.md)
and [Task model](../../docs/distributed-task-model.md).

Prefer the current CLI and enter the secret at its prompt:

```sh
amux agent credential set anthropic ANTHROPIC_API_KEY
amux agent credential show anthropic
amux agent credential ls
```

With an existing enrollment or `AMUX_ACTOR_TOKEN`, commands use the current
Principal's credential store through the Hub. An unreachable Hub fails the
operation; it does not fall back to the service account's global store. To
initialize a credential for a specific Principal, the operator uses:

```sh
amux agent credential setup anthropic ANTHROPIC_API_KEY --principal <principal-id>
```

An unidentified legacy-host uses `$AMUX_HOME/secrets/<profile>.json` (normally
`~/.amux/secrets/`). The [Anthropic](anthropic.json.example) and
[Codex](codex.json.example) JSON templates contain placeholder values only.
Manual template imports apply only to legacy-host storage and do not replace
Principal credential management. Keep real files outside the source repository
with permissions `0600`.

Reference the credential name when submitting:

```sh
amux task submit "Review the code in the working directory" --profile <profile-ref> --credential-profile anthropic --wait
```

Replace `<profile-ref>` with a Profile that declares `ANTHROPIC_API_KEY`. Native
built-in Profiles can also use their Agent's native login method. Creating a
Credential Profile does not add variable declarations to a Pack. `files` keys
are paths relative to the isolated HOME; `kind` is `long-lived` or `short-lived`.
An empty credential name injects no credentials. See the [CLI reference](../../docs/reference/cli.md)
for complete options.

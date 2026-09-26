# Windows Setup (phase 1)

[English](windows-setup.md) | [简体中文](windows-setup.zh-CN.md)

Phase 1 ships a **Setup.exe** installer for Windows amd64. It is not a bash
`install.sh` path.

## What Setup.exe does

1. Copies `amux.exe`, `amuxd.exe`, and `amuxlet.exe` into `%USERPROFILE%\.amux\bin`
   (override with `--bin-dir` / `--amux-home`).
2. Runs Feishu (飞书) bind via the same device-code flow as
   `amux bridge feishu setup` (terminal QR + optional browser open).
   Scan failure, timeout, or network auth failure **aborts setup** with a
   non-zero exit. There is **no** App ID/Secret manual fallback in phase 1.
3. Registers a real **Windows Service** named `Amux` (Automatic start) that
   hosts the hub (`amuxd`) and runtime (`amuxlet`).
4. For singleton mode (default), creates/binds the Feishu bot-session like
   `install.sh --feishu`.

Defaults match `scripts/install.sh --feishu`:
`--feishu-profile default`, `--interaction-mode singleton`,
`--session-name <profile>-main`, `--agent-profile builtin/<runtime>`.

## Windows Service account design

The service runs as **LocalSystem** with start type Automatic. The installer
writes `C:\ProgramData\amux\service.json` with the installing user's
`AMUX_HOME` (normally `%USERPROFILE%\.amux`) and grants SYSTEM access to that
directory via `icacls`. The service host sets `AMUX_HOME`, `USERPROFILE`, and
`APPDATA` for child processes so Feishu secrets under the user profile remain
reachable after reboot.

This prefers boot-time availability for the Feishu long-connection over a
password-gated "run as installing user" service account. Uninstall
(`amux system uninstall` / `amux system service uninstall`) stops and deletes
the service.

CLI: `amux system service install|uninstall|start|stop|status`.

## Local packaging smoke

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 make build VERSION=v0.0.0-test
bash scripts/package-release.sh --version v0.0.0-test --os windows --arch amd64 --source ./bin
```

On a Windows machine (Administrator), extract the tarball and run:

```text
Setup.exe --runtime codex
```

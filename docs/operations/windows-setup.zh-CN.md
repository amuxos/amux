# Windows Setup（phase 1）

[English](windows-setup.md) | [简体中文](windows-setup.zh-CN.md)

Phase 1 为 Windows amd64 提供 **Setup.exe** 安装器，而不是 bash `install.sh`。

## Setup.exe 做什么

1. 将 `amux.exe` / `amuxd.exe` / `amuxlet.exe` 安装到 `%USERPROFILE%\.amux\bin`。
2. 通过与 `amux bridge feishu setup` 相同的设备码流程绑定飞书（终端二维码 + 可选浏览器）。
   扫码失败、超时或网络阻断会导致**安装失败**；phase 1 **没有** App ID/Secret 手工回退。
3. 注册并开机自启真正的 Windows Service（`Amux`），托管 hub（amuxd）与 runtime（amuxlet）。
4. singleton 模式下绑定 bot-session（默认与 `install.sh --feishu` 一致）。

## 服务账户设计

服务以 **LocalSystem**、Automatic 启动；安装器把用户的 `AMUX_HOME` 写入
`C:\ProgramData\amux\service.json`，并用 `icacls` 授予 SYSTEM 访问权限。
服务进程为子进程设置 `AMUX_HOME` / `USERPROFILE` / `APPDATA`，以便读取用户目录下的飞书凭证。

卸载会停止并删除该服务：`amux system service uninstall` 或 `amux system uninstall`。

// Package winsvc installs and runs the Amux Windows Service that keeps the
// hub (amuxd) and runtime (amuxlet) alive across reboots.
//
// Design (phase 1): the service runs as LocalSystem with Automatic start. The
// installer records the installing user's AMUX_HOME (normally
// %USERPROFILE%\.amux) in C:\ProgramData\amux\service.json and grants SYSTEM
// access to that directory. The service host sets AMUX_HOME / USERPROFILE /
// APPDATA for child processes so Feishu secrets and agent credentials under
// the user profile remain reachable after boot, before an interactive logon
// is required for the bot's long-connection.
package winsvc

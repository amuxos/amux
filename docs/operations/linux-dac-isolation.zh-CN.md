# Linux UID/DAC 隔离运维

[English](linux-dac-isolation.md) | [简体中文](linux-dac-isolation.zh-CN.md)

本功能需要 Linux、systemd、root helper 和一段与现有账号不冲突的专用 UID 区间。
helper 将不同 Principal 的实际 Agent 进程以不同 UID 运行；同一 Principal 的任务共享
该 UID 和长期 workspace。产品边界见 [Task 模型](../distributed-task-model.zh-CN.md)。
普通安装不会自动安装 root helper 或开启隔离。

## 安装与配置

先构建同版本的 `amux`、`amuxlet` 和 `amux-isolation-helper` 并安装到同一目录。
以实际 service user 执行 `amux isolation helper install`，它会给出需要审核执行的 sudo
命令。下面以 service UID/GID 1001 为例；先检查整段 UID 区间无冲突，再替换参数执行：

```sh
sudo amux isolation helper install \
  --service-uid 1001 --service-gid 1001 \
  --amux-home /home/amux/.amux \
  --socket /home/amux/.amux/isolation-helper.sock \
  --uid-range-start 700000 --uid-range-end 799999 \
  --principal-root /var/lib/amux/principals
```

它安装 helper、同版本 materializer 和 systemd unit。service user 的 `config.toml`
应使用相同的 socket、UID 区间和 principal root：

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

配置路径必须为绝对路径。执行 `amux system restart` 后检查：

```sh
amux isolation helper status
amux isolation status
amux system target list
```

需要确认 helper ready、实际 backend 及 target capability。namespace/seccomp hardening
在 helper 安装时配置，单独重启普通 system 不会重新应用 helper 的安装参数。

## 身份与凭据

```sh
amux isolation principal create "Alice" --kind human
amux isolation enroll --provider cli --scope local --subject alice --principal <principal-id>
amux agent credential set openai-compat OPENAI_API_KEY
```

启用隔离时，新建请求必须携带可识别身份。Enrollment token 由 CLI 安全保存，agent
运行期间使用注入的 actor credential；不要把 token 放进命令参数、文档或日志。
已识别调用方的凭据按 Principal 存储；hub 不可达时不会回退到 legacy-host secrets。
共享 service 身份需通过 alias、service membership 或 delegation 显式授权。

飞书新会话还依据各群的处理方式选择进程环境：普通话题和连续会话使用普通宿主环境
（passthrough），用户隔离话题才使用 linux-dac。未单独配置的群和私聊沿用 Bot 默认。
启用机器隔离能力不会把所有飞书会话改为用户隔离；已有会话保持原环境，改变模式后
新建会话才能使用新的选择。CLI、Task 等其它入口继续遵守其现有执行策略。

### 飞书连续会话的共享身份

启用 Linux 隔离后，Bot 连续会话和群级“继续同一会话”均使用共享 service principal。
它的 alias 为 `service:<Bot profile 名>`，作用域为该 Bot 的 bridge ID；这里的 profile
是飞书 Bot profile（例如 `default`），不是 Agent Profile（例如 `builtin/codex`）。
独立话题使用个人 Principal；验证独立话题成功不能代替连续会话的身份配置。

在 Hub 主机执行以下操作。先通过 `amux bridge feishu bots` 和
`amux isolation alias list` 确认 Bot 范围和被授权发起者的个人 Principal，
再创建并绑定共享身份：

```sh
amux isolation principal create "Bot shared sessions" --kind service
# 使用上一条命令输出的 service principal ID。
amux isolation alias bind --provider feishu --scope <bridge-id> \
  --subject service:<bot-profile> --principal <service-principal-id>
amux isolation service-member grant --service <service-principal-id> \
  --member <sender-principal-id>
```

若发起者尚未绑定个人身份，先创建 human principal，并用同一 bridge scope、
该发起者在此 Bot 下的 open ID 绑定 alias。只向明确授权的发起者授予 service membership；
群的 `shared` 访问模式不自动授予执行身份权限。配置保存在 Hub 身份库中，立即生效。
共享身份使用自身的 Principal 凭据；缺少模型凭据时按启动错误提示补齐。

报错 `singleton requires service principal alias ... and active sender membership`
表示上述 alias 或成员授权缺失。保持隔离开启并补齐身份配置后，重新发起群消息即可。
切换测试环境到 Linux 隔离后，应分别验收独立话题和连续会话的新建、续聊。


## 升级、撤销和清理

再次执行相同的 helper install 命令升级。存在 active launch 时会拒绝覆盖，等待它们
结束后重试。普通 `amux update` 不代替这一步。
`amux isolation principal disable <principal-id>` 阻止该身份继续执行，并撤销它的活动执行；
长期账号和工作目录保留。先用只读清点：

```sh
amux isolation account list
amux isolation account gc --older-than-days 90
```

GC 真正删除数据需要 `--execute` 和精确的 `--confirm-principal`，详见
[CLI 参考](../reference/cli.zh-CN.md)。不要直接修改 mapping DB 或重用隔离 UID。

卸载 helper 默认保留账号与数据，active launch 存在时拒绝：

```sh
sudo amux isolation helper uninstall --amux-home /home/amux/.amux \
  --socket /home/amux/.amux/isolation-helper.sock
```

`--force` 会终止活动执行。回滚配置为 `isolation.enabled=false` 并重启 system，可让新
execution 使用普通 backend；已保存的 `linux-dac` 对象不会被改写为 passthrough，恢复它们
仍需恢复 helper。

## 诊断

```sh
systemctl status amux-isolation-helper.service
journalctl -u amux-isolation-helper.service -n 200 --no-pager
amux isolation status
amux isolation account list
```

- helper unavailable：检查 socket、service UID、协议版本和配置匹配；已固定为
  `linux-dac` 的对象重启时不会静默降低隔离等级。
- hardening degraded：检查 namespace/seccomp 的宿主支持，同时核对实际 UID/DAC backend。
- Agent 无法执行：检查二进制和符号链接目标的每一级目录是否允许 Principal UID 访问；
  将共享程序安装到合适的系统路径，不放宽整个服务账号 HOME 的权限。
- 凭据不可用：核对当前 Principal 与所选 Profile 声明的变量，通过 credential 命令修复。
- helper crash：检查活动进程与 `isolation_lost` 状态，恢复 helper 后显式重启相关会话。

离线代码门禁为 `scripts/check.sh`。专用 Linux 测试机可另行执行
`sudo -E scripts/test-linux-dac.sh`；该测试会创建临时高位 UID 和账号，不能作为普通本机检查运行。

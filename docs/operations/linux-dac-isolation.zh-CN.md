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

安装命令把 helper 和同版本 materializer 放入 root 所有的 `/usr/local/libexec/amux`，
并注册 systemd unit。首次安装和托管更新都会将这个目录设为 `0755`，不受安装进程的
umask 影响。隔离 UID 可以执行已安装的 helper，无需访问 service user 的 HOME。
service user 的 `config.toml` 应使用相同的 socket、UID 区间和 principal root：

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

`amux update` 会从同一完整发行包自动更新已安装的 helper 和 materializer，通过 root
或 `sudo -n` 执行。原 systemd unit（身份、目录、UID 范围和加固参数）保持不变，
已停止的 helper 不会被启动。磁盘文件及运行中的 helper 都匹配时跳过；未安装 helper
的机器不会因此安装或开启隔离。

空闲 helper 会原子地暂停接收新 launch，直到更新器退出；更新失败或中断会释放保护。
不支持该协议的旧 helper 需要一次性迁移：在维护窗口暂停新请求，等待会话结束后停止
helper，执行 `amux update`，再启动 helper；后续升级会自动管理重启。

存在活跃 launch 时拒绝替换，更新会在普通服务重启前返回失败，现有隔离会话继续运行；
结束后重试 `amux update`。管理员权限不足、发行包缺失组件或 helper 出错也会明确失败。
替换或启动失败时恢复原来的二进制对，并尝试恢复原服务。自定义 unit 或 systemd drop-in
需要管理员维护升级。`amux isolation helper install` 仍用于首次安装及修改隔离配置。
不要仅为升级 helper 而强制终止活动会话。

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

helper 握手成功不代表旧版本支持文件交换。如果文件传输提示要求升级 helper，说明正在
运行的 helper 拒绝了该操作。应同时检查已安装的 helper 和 `amuxlet-materializer`，
通过 `amux update` 自动更新配套组件，或由管理员执行上述安装命令后重试。
当前 helper 使用自身的文件 worker，以 principal UID 读写文件，不依赖 Profile
materializer。不要仅为升级 helper 而强制终止活动会话。

设置 `AMUX_HELPER_UPGRADE_E2E=1` 可启用 `app/cli` 中的
`TestHelperUpgradeRootIntegration`。以 root 执行，并通过
`AMUX_TEST_OLD_HELPER_BINARY` 和 `AMUX_TEST_HELPER_BINARY` 提供不同的新旧构建。
该夹具启动临时 helper 进程，验证实际替换、幂等性和启动失败后的恢复；systemd 控制
由进程控制替代，不会改动已安装的 unit。

需要 root 的 `scripts/test-linux-dac.sh` 包含未使用 UID 下的 inbox/outbox 交换、
30 MB 成果边界，以及特权文件、FIFO 和路径逃逸拒绝验证。
`TestArtifactDeliveryLinuxDACL4` 是单独显式启用的集成夹具，在独立 helper 和专用测试群中
执行真实飞书图片/文件上传、回复、下载回读与清理。Runtime 发现由直接调用真实 helper 的
适配器替代，不启动 Agent 或 Hub/Runtime daemon。只有获得测试消息授权后才可运行，
并要求 root、未使用的数字 `AMUX_TEST_FILE_UID` 和配套的 `AMUX_TEST_HELPER_BINARY`：

```sh
AMUX_ARTIFACT_L4=1 \
AMUX_TEST_FILE_UID=<unused-uid> \
AMUX_TEST_HELPER_BINARY=/path/to/amux-isolation-helper \
AMUX_TEST_FEISHU_CREDENTIAL_FILE=/secure/path/test-bot.json \
AMUX_TEST_FEISHU_CHAT_ID=<dedicated-test-chat> \
go test ./internal/bridge -run '^TestArtifactDeliveryLinuxDACL4$' -count=1 -v
```

凭据文件包含 `app_id` 和 `app_secret`，禁止把内容放入验收产物。夹具清理自身的消息和
临时文件，不修改服务配置，不创建系统账号。

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

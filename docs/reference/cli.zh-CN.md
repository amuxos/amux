<!-- Code generated from app/cli's Cobra command tree; DO NOT EDIT. -->

# amux CLI 命令参考

[English](cli.md) | [简体中文](cli.zh-CN.md)

本文档由当前 `amux` Cobra 命令树自动生成，覆盖全部支持平台上的公开命令、参数、选项、默认值、别名和源码中登记的示例。根命令选项是所有子命令可用的全局选项；平台受限命令会明确标注，隐藏命令与隐藏选项不会被收录，也不构成公开 CLI 契约。

修改公开命令的名称、层级、可见性、说明、参数、选项或默认值后，必须运行 `make cli-doc`；仓库统一门禁会拒绝生成结果不一致的改动。产品语义与行为边界仍以 [`docs/current/cli-product-model.md`](../current/cli-product-model.zh-CN.md) 为准。

## 命令索引

| 命令 | 说明 |
| --- | --- |
| `amux` | 统一管理多个 Runtime 上的代码智能体 |
| `amux agent` | 管理 Agent Profile |
| `amux agent credential` | 管理凭据 Profile |
| `amux agent credential ls` | 列出凭据 Profile |
| `amux agent credential set` | 在凭据 Profile 中保存一个环境变量密钥 |
| `amux agent credential setup` | 为 identified Principal 配置一项凭据 |
| `amux agent credential show` | 显示凭据 Profile 元数据，不显示密钥值 |
| `amux agent doctor` | 诊断 Agent Profile |
| `amux agent list` | 列出可用的 Agent Profile |
| `amux agent pack` | 管理 Agent Profile Pack |
| `amux agent pack add` | 添加基于 Git 的 Agent Pack |
| `amux agent pack autosync` | 管理 Agent Pack 后台自动同步 |
| `amux agent pack autosync logs` | 显示 Agent Pack 后台自动同步日志路径 |
| `amux agent pack autosync start` | 启动 Agent Pack 后台自动同步 |
| `amux agent pack autosync status` | 显示 Agent Pack 后台自动同步状态 |
| `amux agent pack autosync stop` | 停止 Agent Pack 后台自动同步 |
| `amux agent pack ls` | 列出 Agent Pack |
| `amux agent pack remove` | 移除 Agent Pack |
| `amux agent pack rollback` | 将 Pack 回滚到已记录的版本 |
| `amux agent pack sync` | 同步一个或全部 Agent Pack |
| `amux agent pack versions` | 列出已记录的 Pack 版本 |
| `amux agent pack watch` | 持续同步 Agent Pack |
| `amux agent render` | 渲染 Profile 工作区投影 |
| `amux agent show` | 显示 Agent Profile |
| `amux completion` | 为指定 Shell 生成自动补全脚本 |
| `amux completion bash` | 为 bash 生成自动补全脚本 |
| `amux completion fish` | 为 fish 生成自动补全脚本 |
| `amux completion powershell` | 为 powershell 生成自动补全脚本 |
| `amux completion zsh` | 为 zsh 生成自动补全脚本 |
| `amux help` | 查看任意命令的帮助 |
| `amux isolation` | 管理 identified Principal 和 Linux UID/DAC 隔离 |
| `amux isolation account` | 检查目标 Runtime 本地的 Principal 账号映射 |
| `amux isolation account gc` | 预览或显式删除空闲 Principal 账号 |
| `amux isolation account list` | 列出当前 Runtime 已知的映射 |
| `amux isolation alias` | 管理带作用域的 Principal 别名 |
| `amux isolation alias bind` | 将 provider/scope/subject 绑定到 Principal |
| `amux isolation alias list` | 列出带作用域的 Principal 别名 |
| `amux isolation delegation` | 管理 run_as 委托 |
| `amux isolation delegation grant` | 授权 actor 以另一个 Principal 的身份运行 |
| `amux isolation delegation revoke` | 撤销 run_as 委托 |
| `amux isolation enroll` | 创建并保存 CLI actor 凭据 |
| `amux isolation global-env` | 管理 identified 隔离执行共享的环境变量密钥 |
| `amux isolation global-env ls` | 列出共享隔离环境变量名，不显示值 |
| `amux isolation global-env set` | 保存一个安装级隔离环境变量密钥 |
| `amux isolation global-env unset` | 移除一个安装级隔离环境变量密钥 |
| `amux isolation helper` | 安装和管理特权 Linux helper |
| `amux isolation helper install` | 显式安装并启动 root helper 的 systemd 单元 |
| `amux isolation helper status` | 检查本地 helper 握手 |
| `amux isolation helper uninstall` | 停止并移除 helper，默认保留账号数据 |
| `amux isolation principal` | 管理 canonical Principal |
| `amux isolation principal create` | 创建 human 或 service Principal |
| `amux isolation principal disable` | 禁用 Principal 的新执行和授权 |
| `amux isolation principal list` | 列出 Principal |
| `amux isolation service-member` | 管理 service Principal 成员关系 |
| `amux isolation service-member grant` | 允许 human Principal 以 service Principal 身份运行 |
| `amux isolation service-member revoke` | 撤销 service Principal 成员关系及活动服务启动 |
| `amux isolation status` | 显示隔离配置、helper 就绪情况和回退状态 |
| `amux memory` | 通过 Hub 管理共享 Markdown 记忆 |
| `amux memory export` | 将单份完整记忆文档导出到标准输出 |
| `amux memory forget` | 按条件删除记忆文档 |
| `amux memory search` | 使用字面关键词搜索记忆 |
| `amux memory show` | 读取有界记忆文档或指定行段 |
| `amux memory upsert` | 按条件保存 UTF-8 Markdown 记忆文档 |
| `amux schedule` | 定时调度 Prompt 或子任务 |
| `amux schedule create` | 创建定时 Prompt |
| `amux schedule create-task` | 创建定时子任务 |
| `amux schedule disable` | 禁用定时 Prompt |
| `amux schedule enable` | 启用定时 Prompt |
| `amux schedule list` | 列出定时 Prompt 和子任务 |
| `amux schedule remove` | 删除定时 Prompt |
| `amux schedule runs` | 列出某个 Schedule 的运行记录 |
| `amux schedule update` | 原地更新定时 Prompt 或子任务 |
| `amux session` | 管理智能体 Session |
| `amux session attach` | 连接 Session（使用 /detach 或 Ctrl-] 断开） |
| `amux session cwd` | 显示或修改 Session 工作目录 |
| `amux session interrupt` | 中断当前轮，不结束 Session |
| `amux session list` | 列出 Session |
| `amux session prune` | 从默认 target 清理已归档（无效）的 Session |
| `amux session queue` | 显示 Session 的待处理输入队列 |
| `amux session queue cancel` | 取消一条待处理输入 |
| `amux session queue clear` | 清空 Session 的待处理输入队列 |
| `amux session restart` | 重启一个 Session 或全部正在运行的本地用户 Session |
| `amux session resume` | 在当前 Runtime 上恢复休眠 Session |
| `amux session run` | 启动智能体并注册为 Session |
| `amux session send` | 向 Session 发送一条消息，不连接交互界面 |
| `amux session set-model` | 切换 Session 模型并保留对话历史 |
| `amux session sleep` | 停止 Session 进程，保留恢复能力 |
| `amux session stop` | 停止 Session 及其智能体进程 |
| `amux setup` | 配置当前 amux 安装 |
| `amux system` | 检查和管理当前 amux 安装 |
| `amux system config` | 管理 amux 配置 |
| `amux system config get` | 获取配置值 |
| `amux system config path` | 输出配置文件路径 |
| `amux system config set` | 设置配置值 |
| `amux system config show` | 显示配置并隐藏共享环境变量值 |
| `amux system config unset` | 删除配置值 |
| `amux system diagnose` | 关联查询消息、轮次、任务或 Session |
| `amux system doctor` | 诊断当前安装，不修改配置 |
| `amux system logs` | 显示安装日志路径 |
| `amux system operation` | 检查和取消生命周期操作 |
| `amux system operation cancel` | 取消待处理的生命周期操作 |
| `amux system operation list` | 列出生命周期操作 |
| `amux system operation show` | 显示生命周期操作 |
| `amux system restart` | 重启当前 amux 安装 |
| `amux system service` | 管理 Amux Windows 服务（Hub + Runtime） |
| `amux system service install` | 安装 Amux Windows 服务（自动启动） |
| `amux system service start` | 启动 Amux Windows 服务 |
| `amux system service status` | 显示 Amux Windows 服务状态 |
| `amux system service stop` | 停止 Amux Windows 服务 |
| `amux system service uninstall` | 停止并移除 Amux Windows 服务 |
| `amux system start` | 启动当前 amux 安装 |
| `amux system status` | 显示安装状态 |
| `amux system stop` | 停止当前 amux 安装 |
| `amux system target` | 管理 Runtime target |
| `amux system target list` | 列出连接到 Hub 的 Runtime |
| `amux system uninstall` | 卸载当前用户的 amux |
| `amux task` | 运行和管理子智能体任务 |
| `amux task cancel` | 取消任务 |
| `amux task list` | 列出任务 |
| `amux task logs` | 持续读取任务事件直至结束（只读） |
| `amux task prune` | 删除终态任务历史 |
| `amux task respond` | 响应升级的权限请求（escalate-to-main） |
| `amux task show` | 显示任务状态和结果 |
| `amux task submit` | 提交子智能体任务 |

## `amux`

统一管理多个 Runtime 上的代码智能体

**用法**

```text
amux [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux agent` | 管理 Agent Profile |
| `amux completion` | 为指定 Shell 生成自动补全脚本 |
| `amux help` | 查看任意命令的帮助 |
| `amux isolation` | 管理 identified Principal 和 Linux UID/DAC 隔离 |
| `amux memory` | 通过 Hub 管理共享 Markdown 记忆 |
| `amux schedule` | 定时调度 Prompt 或子任务 |
| `amux session` | 管理智能体 Session |
| `amux setup` | 配置当前 amux 安装 |
| `amux system` | 检查和管理当前 amux 安装 |
| `amux task` | 运行和管理子智能体任务 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | amux 帮助 |
| `--json` | `bool` | `false` | 输出稳定的 JSON |
| `-v, --version` | `bool` | `false` | amux 版本 |

## `amux agent`

管理 Agent Profile

**用法**

```text
amux agent [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux agent credential` | 管理凭据 Profile |
| `amux agent doctor` | 诊断 Agent Profile |
| `amux agent list` | 列出可用的 Agent Profile |
| `amux agent pack` | 管理 Agent Profile Pack |
| `amux agent render` | 渲染 Profile 工作区投影 |
| `amux agent show` | 显示 Agent Profile |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | agent 帮助 |

## `amux agent credential`

管理凭据 Profile

**用法**

```text
amux agent credential [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux agent credential ls` | 列出凭据 Profile |
| `amux agent credential set` | 在凭据 Profile 中保存一个环境变量密钥 |
| `amux agent credential setup` | 为 identified Principal 配置一项凭据 |
| `amux agent credential show` | 显示凭据 Profile 元数据，不显示密钥值 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | credential 帮助 |

## `amux agent credential ls`

列出凭据 Profile

**用法**

```text
amux agent credential ls [flags]
```

**别名：** `list`

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | ls 帮助 |

## `amux agent credential set`

在凭据 Profile 中保存一个环境变量密钥

**用法**

```text
amux agent credential set <profile> <env-name> [flags]
```

**示例**

```bash
amux agent credential set openai-compat OPENAI_API_KEY
printf '%s\n' "$OPENAI_API_KEY" | amux agent credential set openai-compat OPENAI_API_KEY
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | set 帮助 |

## `amux agent credential setup`

为 identified Principal 配置一项凭据

**用法**

```text
amux agent credential setup <profile> <env-name> [flags]
```

**示例**

```bash
amux agent credential setup openai-compat GLM_API_KEY --principal <principal-id>
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--from-legacy-host` | `bool` | `false` | 从 legacy-host 凭据存储复制值 |
| `-h, --help` | `bool` | `false` | setup 帮助 |
| `--principal` | `string` | `""` | 目标 Principal UUID（必填） |
| `--rotate-enrollment` | `bool` | `false` | 用目标 Principal 替换本地 CLI 注册身份 |

## `amux agent credential show`

显示凭据 Profile 元数据，不显示密钥值

**用法**

```text
amux agent credential show <profile> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | show 帮助 |

## `amux agent doctor`

诊断 Agent Profile

**用法**

```text
amux agent doctor [profile] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | doctor 帮助 |
| `--model` | `string` | `""` | 在所选 Runtime 中诊断的模型 |
| `--runtime` | `string` | `""` | 使用该 Agent Profile 进行诊断的 Runtime Adapter |
| `--smoke` | `bool` | `false` | 完成静态检查后运行一次真实端到端任务轮次 |
| `--smoke-timeout` | `int` | `120` | 冒烟测试轮次超时秒数 |

## `amux agent list`

列出可用的 Agent Profile

**用法**

```text
amux agent list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | 包含开发版 Profile 命名空间 |
| `--dev` | `bool` | `false` | 仅显示开发版 Profile 命名空间 |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux agent pack`

管理 Agent Profile Pack

**用法**

```text
amux agent pack [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux agent pack add` | 添加基于 Git 的 Agent Pack |
| `amux agent pack autosync` | 管理 Agent Pack 后台自动同步 |
| `amux agent pack ls` | 列出 Agent Pack |
| `amux agent pack remove` | 移除 Agent Pack |
| `amux agent pack rollback` | 将 Pack 回滚到已记录的版本 |
| `amux agent pack sync` | 同步一个或全部 Agent Pack |
| `amux agent pack versions` | 列出已记录的 Pack 版本 |
| `amux agent pack watch` | 持续同步 Agent Pack |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | pack 帮助 |

## `amux agent pack add`

添加基于 Git 的 Agent Pack

**用法**

```text
amux agent pack add <namespace> <git-url> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dev-of` | `string` | `""` | 此开发 Pack 所属的正式 Pack 命名空间 |
| `-h, --help` | `bool` | `false` | add 帮助 |
| `--no-auto-sync` | `bool` | `false` | 登记 Pack 时禁用自动同步 |
| `--ref` | `string` | `""` | 要同步的 Git ref，例如 stable、main 或 tag |
| `--sync-interval` | `string` | `30s` | 为该 Pack 记录的默认自动同步间隔 |
| `--temporary` | `bool` | `false` | 将 --dev-of Pack 标为临时并禁用自动同步 |

## `amux agent pack autosync`

管理 Agent Pack 后台自动同步

**用法**

```text
amux agent pack autosync [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux agent pack autosync logs` | 显示 Agent Pack 后台自动同步日志路径 |
| `amux agent pack autosync start` | 启动 Agent Pack 后台自动同步 |
| `amux agent pack autosync status` | 显示 Agent Pack 后台自动同步状态 |
| `amux agent pack autosync stop` | 停止 Agent Pack 后台自动同步 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | autosync 帮助 |

## `amux agent pack autosync logs`

显示 Agent Pack 后台自动同步日志路径

**用法**

```text
amux agent pack autosync logs [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | logs 帮助 |

## `amux agent pack autosync start`

启动 Agent Pack 后台自动同步

**用法**

```text
amux agent pack autosync start [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | start 帮助 |

## `amux agent pack autosync status`

显示 Agent Pack 后台自动同步状态

**用法**

```text
amux agent pack autosync status [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | status 帮助 |

## `amux agent pack autosync stop`

停止 Agent Pack 后台自动同步

**用法**

```text
amux agent pack autosync stop [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | stop 帮助 |

## `amux agent pack ls`

列出 Agent Pack

**用法**

```text
amux agent pack ls [flags]
```

**别名：** `list`

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | ls 帮助 |

## `amux agent pack remove`

移除 Agent Pack

**用法**

```text
amux agent pack remove <namespace> [flags]
```

**别名：** `rm`、`delete`

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | remove 帮助 |
| `--keep-files` | `bool` | `false` | 注销 Pack，但不删除本地物化文件 |

## `amux agent pack rollback`

将 Pack 回滚到已记录的版本

**用法**

```text
amux agent pack rollback <namespace> <revision> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | rollback 帮助 |

## `amux agent pack sync`

同步一个或全部 Agent Pack

**用法**

```text
amux agent pack sync [namespace] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | sync 帮助 |

## `amux agent pack versions`

列出已记录的 Pack 版本

**用法**

```text
amux agent pack versions <namespace> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | versions 帮助 |

## `amux agent pack watch`

持续同步 Agent Pack

**用法**

```text
amux agent pack watch <namespace> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | watch 帮助 |
| `--interval` | `string` | `30s` | 同步间隔 |

## `amux agent render`

渲染 Profile 工作区投影

**用法**

```text
amux agent render <profile> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | render 帮助 |
| `--model` | `string` | `""` | 为所选 Runtime 渲染的模型 |
| `--out` | `string` | `""` | 输出工作区目录 |
| `--runtime` | `string` | `""` | 渲染使用的 Runtime 适配器；v2 专家必填 |

## `amux agent show`

显示 Agent Profile

**用法**

```text
amux agent show <profile> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | show 帮助 |

## `amux completion`

为指定 Shell 生成自动补全脚本

为指定 Shell 生成 amux 自动补全脚本。
生成脚本的使用方法见各子命令帮助。

**用法**

```text
amux completion [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux completion bash` | 为 bash 生成自动补全脚本 |
| `amux completion fish` | 为 fish 生成自动补全脚本 |
| `amux completion powershell` | 为 powershell 生成自动补全脚本 |
| `amux completion zsh` | 为 zsh 生成自动补全脚本 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | completion 帮助 |

## `amux completion bash`

为 bash 生成自动补全脚本

为 bash Shell 生成自动补全脚本。

该脚本依赖 bash-completion 包。
尚未安装时，可通过操作系统包管理器安装。

在当前 Shell 会话中加载补全：

	source <(amux completion bash)

为后续每个新会话加载补全，只需执行一次：

#### Linux：

	amux completion bash > /etc/bash_completion.d/amux

#### macOS：

	amux completion bash > $(brew --prefix)/etc/bash_completion.d/amux

需要启动新的 Shell 才能生效。

**用法**

```text
amux completion bash
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | bash 帮助 |
| `--no-descriptions` | `bool` | `false` | 禁用补全说明 |

## `amux completion fish`

为 fish 生成自动补全脚本

为 fish Shell 生成自动补全脚本。

在当前 Shell 会话中加载补全：

	amux completion fish | source

为后续每个新会话加载补全，只需执行一次：

	amux completion fish > ~/.config/fish/completions/amux.fish

需要启动新的 Shell 才能生效。

**用法**

```text
amux completion fish [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | fish 帮助 |
| `--no-descriptions` | `bool` | `false` | 禁用补全说明 |

## `amux completion powershell`

为 powershell 生成自动补全脚本

为 powershell 生成自动补全脚本。

在当前 Shell 会话中加载补全：

	amux completion powershell | Out-String | Invoke-Expression

要在每个新会话中加载补全，将上述命令的输出
加入 powershell Profile。

**用法**

```text
amux completion powershell [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | powershell 帮助 |
| `--no-descriptions` | `bool` | `false` | 禁用补全说明 |

## `amux completion zsh`

为 zsh 生成自动补全脚本

为 zsh Shell 生成自动补全脚本。

如果当前环境尚未启用 Shell 补全，
可执行以下命令启用，只需执行一次：

	echo "autoload -U compinit; compinit" >> ~/.zshrc

在当前 Shell 会话中加载补全：

	source <(amux completion zsh)

为后续每个新会话加载补全，只需执行一次：

#### Linux：

	amux completion zsh > "${fpath[1]}/_amux"

#### macOS：

	amux completion zsh > $(brew --prefix)/share/zsh/site-functions/_amux

需要启动新的 Shell 才能生效。

**用法**

```text
amux completion zsh [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | zsh 帮助 |
| `--no-descriptions` | `bool` | `false` | 禁用补全说明 |

## `amux help`

查看任意命令的帮助

Help 提供应用中任意命令的帮助。
输入 amux help [命令路径] 即可查看完整说明。

**用法**

```text
amux help [command] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | help 帮助 |

## `amux isolation`

管理 identified Principal 和 Linux UID/DAC 隔离

**用法**

```text
amux isolation [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation account` | 检查目标 Runtime 本地的 Principal 账号映射 |
| `amux isolation alias` | 管理带作用域的 Principal 别名 |
| `amux isolation delegation` | 管理 run_as 委托 |
| `amux isolation enroll` | 创建并保存 CLI actor 凭据 |
| `amux isolation global-env` | 管理 identified 隔离执行共享的环境变量密钥 |
| `amux isolation helper` | 安装和管理特权 Linux helper |
| `amux isolation principal` | 管理 canonical Principal |
| `amux isolation service-member` | 管理 service Principal 成员关系 |
| `amux isolation status` | 显示隔离配置、helper 就绪情况和回退状态 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | isolation 帮助 |

## `amux isolation account`

检查目标 Runtime 本地的 Principal 账号映射

**用法**

```text
amux isolation account [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation account gc` | 预览或显式删除空闲 Principal 账号 |
| `amux isolation account list` | 列出当前 Runtime 已知的映射 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | account 帮助 |

## `amux isolation account gc`

预览或显式删除空闲 Principal 账号

**用法**

```text
amux isolation account gc [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--confirm-principal` | `string` | `""` | 要删除的准确 Principal UUID |
| `--execute` | `bool` | `false` | 执行不可逆的账号、HOME 和工作区删除 |
| `-h, --help` | `bool` | `false` | gc 帮助 |
| `--older-than-days` | `int` | `0` | 空闲时长阈值（默认 isolation.gc_idle_days） |

## `amux isolation account list`

列出当前 Runtime 已知的映射

**用法**

```text
amux isolation account list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |
| `--json` | `bool` | `false` | 输出 JSON |

## `amux isolation alias`

管理带作用域的 Principal 别名

**用法**

```text
amux isolation alias [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation alias bind` | 将 provider/scope/subject 绑定到 Principal |
| `amux isolation alias list` | 列出带作用域的 Principal 别名 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | alias 帮助 |

## `amux isolation alias bind`

将 provider/scope/subject 绑定到 Principal

**用法**

```text
amux isolation alias bind [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | bind 帮助 |
| `--principal` | `string` | `""` | Principal UUID（必填） |
| `--provider` | `string` | `cli` | 别名的 provider |
| `--scope` | `string` | `local` | provider 本地作用域 |
| `--subject` | `string` | `""` | provider subject（必填） |

## `amux isolation alias list`

列出带作用域的 Principal 别名

**用法**

```text
amux isolation alias list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux isolation delegation`

管理 run_as 委托

**用法**

```text
amux isolation delegation [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation delegation grant` | 授权 actor 以另一个 Principal 的身份运行 |
| `amux isolation delegation revoke` | 撤销 run_as 委托 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | delegation 帮助 |

## `amux isolation delegation grant`

授权 actor 以另一个 Principal 的身份运行

**用法**

```text
amux isolation delegation grant <actor-principal-id> <effective-principal-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | grant 帮助 |

## `amux isolation delegation revoke`

撤销 run_as 委托

**用法**

```text
amux isolation delegation revoke <delegation-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | revoke 帮助 |

## `amux isolation enroll`

创建并保存 CLI actor 凭据

**用法**

```text
amux isolation enroll [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | enroll 帮助 |
| `--principal` | `string` | `""` | Principal UUID（必填） |
| `--provider` | `string` | `cli` | actor provider |
| `--rotate` | `bool` | `false` | 替换已有的本地注册凭据 |
| `--scope` | `string` | `local` | provider 本地作用域 |
| `--subject` | `string` | `""` | provider subject（默认当前用户） |

## `amux isolation global-env`

管理 identified 隔离执行共享的环境变量密钥

**用法**

```text
amux isolation global-env [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation global-env ls` | 列出共享隔离环境变量名，不显示值 |
| `amux isolation global-env set` | 保存一个安装级隔离环境变量密钥 |
| `amux isolation global-env unset` | 移除一个安装级隔离环境变量密钥 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | global-env 帮助 |

## `amux isolation global-env ls`

列出共享隔离环境变量名，不显示值

**用法**

```text
amux isolation global-env ls [flags]
```

**别名：** `list`

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | ls 帮助 |

## `amux isolation global-env set`

保存一个安装级隔离环境变量密钥

**用法**

```text
amux isolation global-env set <env-name> [flags]
```

**示例**

```bash
amux isolation global-env set GLM_API_KEY
printf '%s\n' "$GLM_API_KEY" | amux isolation global-env set GLM_API_KEY
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | set 帮助 |

## `amux isolation global-env unset`

移除一个安装级隔离环境变量密钥

**用法**

```text
amux isolation global-env unset <env-name> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | unset 帮助 |

## `amux isolation helper`

安装和管理特权 Linux helper

**适用平台：** `linux`

**用法**

```text
amux isolation helper [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation helper install` | 显式安装并启动 root helper 的 systemd 单元 |
| `amux isolation helper status` | 检查本地 helper 握手 |
| `amux isolation helper uninstall` | 停止并移除 helper，默认保留账号数据 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | helper 帮助 |

## `amux isolation helper install`

显式安装并启动 root helper 的 systemd 单元

**适用平台：** `linux`

**用法**

```text
amux isolation helper install [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `runtime-dependent` | 服务用户的 AMUX_HOME |
| `-h, --help` | `bool` | `false` | install 帮助 |
| `--mount-namespace` | `bool` | `true` | 启用 mount namespace 加固 |
| `--pid-namespace` | `bool` | `true` | 启用 PID namespace 加固 |
| `--principal-root` | `string` | `/var/lib/amux/principals` | Principal 数据根目录 |
| `--seccomp` | `bool` | `true` | 启用 provider seccomp 加固 |
| `--service-gid` | `int` | `runtime-dependent` | amuxlet 服务 GID |
| `--service-uid` | `int` | `runtime-dependent` | 允许连接的 amuxlet 服务 UID |
| `--socket` | `string` | `""` | helper socket 的绝对路径（默认 <amux-home>/isolation-helper.sock） |
| `--uid-range-end` | `int` | `799999` | 专用 Principal UID 范围的上界 |
| `--uid-range-start` | `int` | `700000` | 专用 Principal UID 范围的下界 |

## `amux isolation helper status`

检查本地 helper 握手

**适用平台：** `linux`

**用法**

```text
amux isolation helper status [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | status 帮助 |

## `amux isolation helper uninstall`

停止并移除 helper，默认保留账号数据

**适用平台：** `linux`

**用法**

```text
amux isolation helper uninstall [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `runtime-dependent` | 服务用户的 AMUX_HOME |
| `--force` | `bool` | `false` | 卸载前结束活动的隔离执行 |
| `-h, --help` | `bool` | `false` | uninstall 帮助 |
| `--purge` | `bool` | `false` | 请求破坏性数据清除（需要单独人工审核） |
| `--socket` | `string` | `""` | helper socket 的绝对路径（默认 <amux-home>/isolation-helper.sock） |

## `amux isolation principal`

管理 canonical Principal

**用法**

```text
amux isolation principal [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation principal create` | 创建 human 或 service Principal |
| `amux isolation principal disable` | 禁用 Principal 的新执行和授权 |
| `amux isolation principal list` | 列出 Principal |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | principal 帮助 |

## `amux isolation principal create`

创建 human 或 service Principal

**用法**

```text
amux isolation principal create <display-name> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | create 帮助 |
| `--json` | `bool` | `false` | 输出 JSON |
| `--kind` | `string` | `human` | Principal 类型：human 或 service |
| `--target` | `string` | `""` | 可选的固定环境 target ID |

## `amux isolation principal disable`

禁用 Principal 的新执行和授权

**用法**

```text
amux isolation principal disable <principal-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | disable 帮助 |

## `amux isolation principal list`

列出 Principal

**用法**

```text
amux isolation principal list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |
| `--json` | `bool` | `false` | 输出 JSON |

## `amux isolation service-member`

管理 service Principal 成员关系

**用法**

```text
amux isolation service-member [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux isolation service-member grant` | 允许 human Principal 以 service Principal 身份运行 |
| `amux isolation service-member revoke` | 撤销 service Principal 成员关系及活动服务启动 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | service-member 帮助 |

## `amux isolation service-member grant`

允许 human Principal 以 service Principal 身份运行

**用法**

```text
amux isolation service-member grant [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | grant 帮助 |
| `--member` | `string` | `""` | 成员 Principal UUID（必填） |
| `--service` | `string` | `""` | service Principal UUID（必填） |

## `amux isolation service-member revoke`

撤销 service Principal 成员关系及活动服务启动

**用法**

```text
amux isolation service-member revoke [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | revoke 帮助 |
| `--member` | `string` | `""` | 成员 Principal UUID（必填） |
| `--service` | `string` | `""` | service Principal UUID（必填） |

## `amux isolation status`

显示隔离配置、helper 就绪情况和回退状态

**用法**

```text
amux isolation status [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | status 帮助 |
| `--json` | `bool` | `false` | 输出 JSON |

## `amux memory`

通过 Hub 管理共享 Markdown 记忆

在 Hub 选择的作用域中搜索、读取、按条件更新和删除 Markdown 记忆，无需 Agent Profile。除 export 将单份完整文档写到标准输出外，命令均返回 JSON。

**用法**

```text
amux memory [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux memory export` | 将单份完整记忆文档导出到标准输出 |
| `amux memory forget` | 按条件删除记忆文档 |
| `amux memory search` | 使用字面关键词搜索记忆 |
| `amux memory show` | 读取有界记忆文档或指定行段 |
| `amux memory upsert` | 按条件保存 UTF-8 Markdown 记忆文档 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | memory 的帮助 |

## `amux memory export`

将单份完整记忆文档导出到标准输出

**用法**

```text
amux memory export <path> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | export 的帮助 |

## `amux memory forget`

按条件删除记忆文档

**用法**

```text
amux memory forget <path> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--expected-revision` | `string` | `""` | 之前读取的 SHA-256 版本，已不存在时使用 absent（必填） |
| `-h, --help` | `bool` | `false` | forget 的帮助 |

## `amux memory search`

使用字面关键词搜索记忆

**用法**

```text
amux memory search <keywords> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | search 的帮助 |
| `--limit` | `int32` | `5` | 最多返回的命中文档数（1-20；0 使用服务端默认值） |
| `--path-prefix` | `string` | `""` | 将搜索限定到记忆中的相对路径前缀 |

## `amux memory show`

读取有界记忆文档或指定行段

**用法**

```text
amux memory show <path> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--end-line` | `int32` | `0` | 读取末行（包含；0 读取到响应上限） |
| `-h, --help` | `bool` | `false` | show 帮助 |
| `--offset` | `int64` | `0` | 上次响应通过 next_offset 返回的字节位置 |
| `--start-line` | `int32` | `0` | 读取起始行（从 1 开始；0 从开头读取） |

## `amux memory upsert`

按条件保存 UTF-8 Markdown 记忆文档

**用法**

```text
amux memory upsert <path> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--expected-revision` | `string` | `""` | 之前读取的 SHA-256 版本，新建使用 absent（必填） |
| `--file` | `string` | `-` | 从此文件读取文档正文（- 读取标准输入） |
| `-h, --help` | `bool` | `false` | upsert 的帮助 |

## `amux schedule`

定时调度 Prompt 或子任务

管理定时 Prompt 和定时子任务。到达指定时间时，
amuxd 向目标 Session 注入 Prompt（等同于用户输入），或
按已保存的任务规格提交临时子任务。Schedule 保存在 Hub
（amuxd）中，因此必须配置 Hub 地址。

时间规则格式：
  every <duration>   例如 "every 30m"、"every 2h"（最短 1m）
  daily <HH:MM>      例如 "daily 09:00"（服务器本地时间）
  at <when>          例如 "at 2026-06-01 09:00"（仅执行一次）
  cron <expr>        例如 "cron 0 9 * * 1-5"（标准五字段）

**用法**

```text
amux schedule [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux schedule create` | 创建定时 Prompt |
| `amux schedule create-task` | 创建定时子任务 |
| `amux schedule disable` | 禁用定时 Prompt |
| `amux schedule enable` | 启用定时 Prompt |
| `amux schedule list` | 列出定时 Prompt 和子任务 |
| `amux schedule remove` | 删除定时 Prompt |
| `amux schedule runs` | 列出某个 Schedule 的运行记录 |
| `amux schedule update` | 原地更新定时 Prompt 或子任务 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | schedule 帮助 |

## `amux schedule create`

创建定时 Prompt

**用法**

```text
amux schedule create <session-ref> <spec> <prompt> [flags]
```

**示例**

```bash
amux schedule create local/c1 "every 30m" "/daily-report run"
  amux schedule create local/c1 "at 2026-06-01 09:00" "good morning"
  amux schedule create local/c1 "cron 0 9 * * 1-5" "standup reminder"
  amux schedule create local/c1 "every 1h" "/daily-report run" --run-mode new-session-per-run
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | create 帮助 |
| `--name` | `string` | `""` | Schedule 的可选标签 |
| `--run-mode` | `string` | `reuse-session` | 执行模式：reuse-session 或 new-session-per-run |

## `amux schedule create-task`

创建定时子任务

**用法**

```text
amux schedule create-task <spec> [flags]
```

**示例**

```bash
amux schedule create-task "every 1h" --agent team/reviewer --task "review recent changes"
  amux schedule create-task "daily 09:00" --agent builtin/codex --task "run smoke tests" --workspace-policy git-worktree --local-dir /path/to/repo
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--agent` | `string` | `""` | 要运行的 Agent Profile 引用或旧版 Agent 类型 |
| `--arg` | `stringArray` | `[]` | 原生 Agent 参数（可重复） |
| `--credential-profile` | `string` | `""` | 凭据 Profile 名称（由控制面解析） |
| `-h, --help` | `bool` | `false` | create-task 帮助 |
| `--isolation` | `string` | `""` | 隔离模式：process \| private-home（默认 process） |
| `--local-dir` | `string` | `""` | 覆盖目标机器上的工作目录 |
| `--model` | `string` | `""` | 执行模型引用；v2 专家省略时使用 Runtime 默认模型，旧版专家仍遵守其原有约束 |
| `--name` | `string` | `""` | Schedule 的可选标签 |
| `--permission-policy` | `string` | `""` | 权限策略：default、policy-allow 或 deny-all |
| `--repo-cache-key` | `string` | `""` | 工作区仓库缓存键 |
| `--repo-commit` | `string` | `""` | 工作区仓库提交或 ref |
| `--repo-kind` | `string` | `""` | 工作区仓库类型（例如 git） |
| `--repo-remote` | `string` | `""` | 工作区仓库远端 URL 或稳定 ID |
| `--repo-subdir` | `string` | `""` | 工作区仓库子目录 |
| `--run-as` | `string` | `""` | 以已注册的 Principal 别名运行（保存 Schedule 时解析并冻结） |
| `--runtime` | `string` | `""` | 执行 Runtime 适配器；v2 专家必填 |
| `--task` | `string` | `""` | 每次 Schedule 触发时执行的任务 Prompt |
| `--timeout` | `int` | `0` | 任务超时秒数（0 = Runtime 默认值） |
| `--trusted-mode-ack` | `bool` | `true` | 旧版空操作：弱隔离下默认注入长期密钥（v0.38） |
| `--workspace-policy` | `string` | `temp-dir` | 工作区策略：temp-dir \| git-worktree |

## `amux schedule disable`

禁用定时 Prompt

**用法**

```text
amux schedule disable <id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | disable 帮助 |

## `amux schedule enable`

启用定时 Prompt

**用法**

```text
amux schedule enable <id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | enable 帮助 |

## `amux schedule list`

列出定时 Prompt 和子任务

**用法**

```text
amux schedule list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux schedule remove`

删除定时 Prompt

**用法**

```text
amux schedule remove <id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | remove 帮助 |

## `amux schedule runs`

列出某个 Schedule 的运行记录

**用法**

```text
amux schedule runs <id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | runs 帮助 |

## `amux schedule update`

原地更新定时 Prompt 或子任务

**用法**

```text
amux schedule update <id> [flags]
```

**示例**

```bash
amux schedule update 8 --name nightly-report
  amux schedule update 8 --spec "daily 09:00"
  amux schedule update 8 --agent builtin/codex --task "run smoke tests"
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--agent` | `string` | `""` | 要运行的 Agent Profile 引用或旧版 Agent 类型 |
| `--arg` | `stringArray` | `[]` | 原生 Agent 参数（可重复） |
| `--credential-profile` | `string` | `""` | 凭据 Profile 名称（由控制面解析） |
| `-h, --help` | `bool` | `false` | update 帮助 |
| `--isolation` | `string` | `""` | 隔离模式：process \| private-home（默认 process） |
| `--local-dir` | `string` | `""` | 覆盖目标机器上的工作目录 |
| `--model` | `string` | `""` | 执行模型引用；v2 专家省略时使用 Runtime 默认模型，旧版专家仍遵守其原有约束 |
| `--name` | `string` | `""` | Schedule 的新标签 |
| `--permission-policy` | `string` | `""` | 权限策略：default、policy-allow 或 deny-all |
| `--repo-cache-key` | `string` | `""` | 工作区仓库缓存键 |
| `--repo-commit` | `string` | `""` | 工作区仓库提交或 ref |
| `--repo-kind` | `string` | `""` | 工作区仓库类型（例如 git） |
| `--repo-remote` | `string` | `""` | 工作区仓库远端 URL 或稳定 ID |
| `--repo-subdir` | `string` | `""` | 工作区仓库子目录 |
| `--run-as` | `string` | `""` | 以已注册的 Principal 别名运行（保存 Schedule 时解析并冻结） |
| `--runtime` | `string` | `""` | 执行 Runtime 适配器；v2 专家必填 |
| `--spec` | `string` | `""` | 新的时间规则（every <duration>、daily <HH:MM>、at <when> 或 cron <expr>）；重新计算下次运行时间 |
| `--task` | `string` | `""` | 每次 Schedule 触发时执行的任务 Prompt |
| `--timeout` | `int` | `0` | 任务超时秒数（0 = Runtime 默认值） |
| `--trusted-mode-ack` | `bool` | `true` | 旧版空操作：弱隔离下默认注入长期密钥（v0.38） |
| `--workspace-policy` | `string` | `temp-dir` | 工作区策略：temp-dir \| git-worktree |

## `amux session`

管理智能体 Session

**用法**

```text
amux session [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux session attach` | 连接 Session（使用 /detach 或 Ctrl-] 断开） |
| `amux session cwd` | 显示或修改 Session 工作目录 |
| `amux session interrupt` | 中断当前轮，不结束 Session |
| `amux session list` | 列出 Session |
| `amux session prune` | 从默认 target 清理已归档（无效）的 Session |
| `amux session queue` | 显示 Session 的待处理输入队列 |
| `amux session restart` | 重启一个 Session 或全部正在运行的本地用户 Session |
| `amux session resume` | 在当前 Runtime 上恢复休眠 Session |
| `amux session run` | 启动智能体并注册为 Session |
| `amux session send` | 向 Session 发送一条消息，不连接交互界面 |
| `amux session set-model` | 切换 Session 模型并保留对话历史 |
| `amux session sleep` | 停止 Session 进程，保留恢复能力 |
| `amux session stop` | 停止 Session 及其智能体进程 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | session 帮助 |

## `amux session attach`

连接 Session（使用 /detach 或 Ctrl-] 断开）

**用法**

```text
amux session attach <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--debug` | `bool` | `false` | 将原始 stdin 字节输出到 stderr |
| `-h, --help` | `bool` | `false` | attach 帮助 |

## `amux session cwd`

显示或修改 Session 工作目录

**用法**

```text
amux session cwd <session-ref> [path] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--after-turn` | `bool` | `false` | 将 cwd 切换排在活动轮次之后，并立即返回 |
| `-h, --help` | `bool` | `false` | cwd 帮助 |
| `--interrupt` | `bool` | `false` | 中断活动轮次后修改 cwd |
| `--no-wait` | `bool` | `false` | 活动轮次未结束时直接失败，不等待 |

## `amux session interrupt`

中断当前轮，不结束 Session

**用法**

```text
amux session interrupt <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | interrupt 帮助 |

## `amux session list`

列出 Session

**用法**

```text
amux session list [target] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | 显示全部 Session，包括任务子 Session |
| `-h, --help` | `bool` | `false` | list 帮助 |
| `--include-tasks` | `bool` | `false` | 包含任务子 Session |

## `amux session prune`

从默认 target 清理已归档（无效）的 Session

**用法**

```text
amux session prune [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | prune 帮助 |

## `amux session queue`

显示 Session 的待处理输入队列

**用法**

```text
amux session queue <session-ref> [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux session queue cancel` | 取消一条待处理输入 |
| `amux session queue clear` | 清空 Session 的待处理输入队列 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | queue 帮助 |

## `amux session queue cancel`

取消一条待处理输入

**用法**

```text
amux session queue cancel <session-ref> <queue-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | cancel 帮助 |

## `amux session queue clear`

清空 Session 的待处理输入队列

**用法**

```text
amux session queue clear <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | clear 帮助 |

## `amux session restart`

重启一个 Session 或全部正在运行的本地用户 Session

**用法**

```text
amux session restart <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | restart 帮助 |
| `--refresh-profile` | `bool` | `false` | 从当前 Agent Profile 版本刷新 Session，并启动新的 provider 上下文 |
| `--running` | `bool` | `false` | 重启全部正在运行的本地用户 Session |

## `amux session resume`

在当前 Runtime 上恢复休眠 Session

**用法**

```text
amux session resume <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | resume 帮助 |

## `amux session run`

启动智能体并注册为 Session

**用法**

```text
amux session run <agent-profile|agent-type> [-- <agent-args>...] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-e, --env` | `stringArray` | `[]` | 设置环境变量 KEY=VAL（可重复） |
| `-h, --help` | `bool` | `false` | run 帮助 |
| `--model` | `string` | `""` | 覆盖此 Session 的 Agent Profile 模型 |
| `-n, --name` | `string` | `""` | Session 名称（必填） |
| `--on` | `string` | `""` | 运行目标（默认 local） |
| `--ref` | `string` | `""` | 物化 --repo 后检出的 Git ref |
| `--repo` | `string` | `""` | 物化到 ./repo 的远端 Git 仓库 URL |
| `--run-as` | `string` | `""` | 以已注册的 Principal 别名运行（由 amuxd 解析） |
| `--runtime` | `string` | `""` | 执行该 Agent Profile 的 Runtime Adapter |

## `amux session send`

向 Session 发送一条消息，不连接交互界面

**用法**

```text
amux session send <session-ref> <text> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | send 帮助 |
| `--interrupt` | `bool` | `false` | 中断当前轮，将本消息排在队列中的输入之前运行 |

## `amux session set-model`

切换 Session 模型并保留对话历史

切换 Session 使用的模型，同时保留其 amux Session
标识、工作区和对话历史。

模型可以是原生模型名，也可以是 Session 的 Agent Profile 中声明的
provider 模型引用（例如 custom/example-model）；直接使用
上游模型名（例如 example-model）时，如果没有歧义，
会解析为对应的 provider 选项。切换到 provider 模型，或在
不同 provider 模型之间切换，会创建新的 provider 对话，
并通过 amux 对话交接携带历史。

Session 空闲且 Runtime 支持原地重配置时
（兼容 Codex 的 app-server、Agent Profile 不变且目标模型为原生模型），
会直接切换模型，无需重启智能体进程。其它情况下，Session
会原地重启，并通过 provider 的原生恢复机制延续对话；
Runtime 不支持原生恢复时，
改用 amux 对话交接。

Task 或 AgentInstance 拥有的 Session 不支持 set-model。

**用法**

```text
amux session set-model <session-ref> <model> [flags]
```

**示例**

```bash
amux session set-model mysession gpt-5.1-codex-mini
  amux session set-model mysession custom/example-model
  amux session set-model remote/mysession claude-sonnet-4-5
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | set-model 帮助 |

## `amux session sleep`

停止 Session 进程，保留恢复能力

**用法**

```text
amux session sleep <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | sleep 帮助 |

## `amux session stop`

停止 Session 及其智能体进程

**用法**

```text
amux session stop <session-ref> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | stop 帮助 |

## `amux setup`

配置当前 amux 安装

**用法**

```text
amux setup [local|hub|remote|feishu|feishu-local] [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--agent` | `string` | `builtin/codex` | feishu-local 使用的 Agent Profile |
| `-h, --help` | `bool` | `false` | setup 帮助 |
| `--hub` | `string` | `""` | remote Profile 使用的 Hub 地址 |
| `--no-start` | `bool` | `false` | 仅写入配置，不启动 Runtime 或 Hub |
| `--smoke` | `bool` | `false` | 配置完成后用一次真实任务轮次验证默认 Profile |
| `--smoke-timeout` | `int` | `120` | 配置冒烟测试轮次超时秒数 |

## `amux system`

检查和管理当前 amux 安装

**用法**

```text
amux system [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux system config` | 管理 amux 配置 |
| `amux system diagnose` | 关联查询消息、轮次、任务或 Session |
| `amux system doctor` | 诊断当前安装，不修改配置 |
| `amux system logs` | 显示安装日志路径 |
| `amux system operation` | 检查和取消生命周期操作 |
| `amux system restart` | 重启当前 amux 安装 |
| `amux system service` | 管理 Amux Windows 服务（Hub + Runtime） |
| `amux system start` | 启动当前 amux 安装 |
| `amux system status` | 显示安装状态 |
| `amux system stop` | 停止当前 amux 安装 |
| `amux system target` | 管理 Runtime target |
| `amux system uninstall` | 卸载当前用户的 amux |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | system 帮助 |

## `amux system config`

管理 amux 配置

**用法**

```text
amux system config [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux system config get` | 获取配置值 |
| `amux system config path` | 输出配置文件路径 |
| `amux system config set` | 设置配置值 |
| `amux system config show` | 显示配置并隐藏共享环境变量值 |
| `amux system config unset` | 删除配置值 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | config 帮助 |

## `amux system config get`

获取配置值

**用法**

```text
amux system config get <key> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | get 帮助 |

## `amux system config path`

输出配置文件路径

**用法**

```text
amux system config path [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | path 帮助 |

## `amux system config set`

设置配置值

**用法**

```text
amux system config set <key> <value> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | set 帮助 |

## `amux system config show`

显示配置并隐藏共享环境变量值

**用法**

```text
amux system config show [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | show 帮助 |

## `amux system config unset`

删除配置值

**用法**

```text
amux system config unset <key> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | unset 帮助 |

## `amux system diagnose`

关联查询消息、轮次、任务或 Session

**用法**

```text
amux system diagnose [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | diagnose 帮助 |
| `--message-id` | `string` | `""` | 飞书消息 ID |
| `--session-id` | `string` | `""` | amux Session ID |
| `--task-id` | `string` | `""` | 分布式 Task ID |
| `--turn-id` | `string` | `""` | amux Turn ID |

## `amux system doctor`

诊断当前安装，不修改配置

**用法**

```text
amux system doctor [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | doctor 帮助 |

## `amux system logs`

显示安装日志路径

**用法**

```text
amux system logs [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | logs 帮助 |

## `amux system operation`

检查和取消生命周期操作

**用法**

```text
amux system operation [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux system operation cancel` | 取消待处理的生命周期操作 |
| `amux system operation list` | 列出生命周期操作 |
| `amux system operation show` | 显示生命周期操作 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | operation 帮助 |

## `amux system operation cancel`

取消待处理的生命周期操作

**用法**

```text
amux system operation cancel <operation-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | cancel 帮助 |

## `amux system operation list`

列出生命周期操作

**用法**

```text
amux system operation list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux system operation show`

显示生命周期操作

**用法**

```text
amux system operation show <operation-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | show 帮助 |

## `amux system restart`

重启当前 amux 安装

**用法**

```text
amux system restart [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | 即使会中断活动的本地工作，也继续执行 |
| `-h, --help` | `bool` | `false` | restart 帮助 |
| `--plan` | `bool` | `false` | 预览生命周期操作，不修改任何内容 |

## `amux system service`

管理 Amux Windows 服务（Hub + Runtime）

**用法**

```text
amux system service [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux system service install` | 安装 Amux Windows 服务（自动启动） |
| `amux system service start` | 启动 Amux Windows 服务 |
| `amux system service status` | 显示 Amux Windows 服务状态 |
| `amux system service stop` | 停止 Amux Windows 服务 |
| `amux system service uninstall` | 停止并移除 Amux Windows 服务 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | service 帮助 |

## `amux system service install`

安装 Amux Windows 服务（自动启动）

**用法**

```text
amux system service install [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--amux-home` | `string` | `""` | amux 数据目录（默认：当前 AMUX_HOME 或 %USERPROFILE%\.amux） |
| `--bin-dir` | `string` | `""` | 包含 amux.exe / amuxd.exe / amuxlet.exe 的目录（默认：当前 amux 所在目录） |
| `-h, --help` | `bool` | `false` | install 帮助 |

## `amux system service start`

启动 Amux Windows 服务

**用法**

```text
amux system service start [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | start 帮助 |

## `amux system service status`

显示 Amux Windows 服务状态

**用法**

```text
amux system service status [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | status 帮助 |

## `amux system service stop`

停止 Amux Windows 服务

**用法**

```text
amux system service stop [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | stop 帮助 |

## `amux system service uninstall`

停止并移除 Amux Windows 服务

**用法**

```text
amux system service uninstall [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | uninstall 帮助 |

## `amux system start`

启动当前 amux 安装

**用法**

```text
amux system start [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | start 帮助 |
| `--plan` | `bool` | `false` | 预览生命周期操作，不修改任何内容 |

## `amux system status`

显示安装状态

**用法**

```text
amux system status [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | status 帮助 |

## `amux system stop`

停止当前 amux 安装

**用法**

```text
amux system stop [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | 即使会中断活动的本地工作，也继续执行 |
| `-h, --help` | `bool` | `false` | stop 帮助 |
| `--plan` | `bool` | `false` | 预览生命周期操作，不修改任何内容 |

## `amux system target`

管理 Runtime target

**用法**

```text
amux system target [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux system target list` | 列出连接到 Hub 的 Runtime |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | target 帮助 |

## `amux system target list`

列出连接到 Hub 的 Runtime

**用法**

```text
amux system target list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux system uninstall`

卸载当前用户的 amux

**用法**

```text
amux system uninstall [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--bin-dir` | `string` | `""` | 已安装的二进制目录（默认 ~/.amux/bin） |
| `-h, --help` | `bool` | `false` | uninstall 帮助 |
| `--purge` | `bool` | `false` | 删除 ~/.amux 中的数据、配置、日志和密钥 |
| `--yes` | `bool` | `false` | 确认卸载 |

## `amux task`

运行和管理子智能体任务

提交和管理子智能体任务。每个任务在独立的运行沙箱中使用凭据 Profile 和权限策略执行智能体，并返回结构化结果。CLI 向 Hub 提交任务，由 Hub 调度至已注册的 Runtime。

**用法**

```text
amux task [flags]
```

**子命令**

| 命令 | 说明 |
| --- | --- |
| `amux task cancel` | 取消任务 |
| `amux task list` | 列出任务 |
| `amux task logs` | 持续读取任务事件直至结束（只读） |
| `amux task prune` | 删除终态任务历史 |
| `amux task respond` | 响应升级的权限请求（escalate-to-main） |
| `amux task show` | 显示任务状态和结果 |
| `amux task submit` | 提交子智能体任务 |

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | task 帮助 |

## `amux task cancel`

取消任务

**用法**

```text
amux task cancel <task-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | cancel 帮助 |

## `amux task list`

列出任务

**用法**

```text
amux task list [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | list 帮助 |

## `amux task logs`

持续读取任务事件直至结束（只读）

**用法**

```text
amux task logs <task-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | logs 帮助 |

## `amux task prune`

删除终态任务历史

**用法**

```text
amux task prune [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dry-run` | `bool` | `false` | 预览将删除的内容，不实际删除 |
| `-h, --help` | `bool` | `false` | prune 帮助 |
| `--older-than` | `string` | `""` | 仅清理早于指定时长的终态任务（例如 24h 或 7d） |
| `--status` | `string` | `""` | 要清理的终态状态，以逗号分隔（succeeded,failed,cancelled,timed_out） |

## `amux task respond`

响应升级的权限请求（escalate-to-main）

**用法**

```text
amux task respond <task-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--allow` | `bool` | `false` | 批准请求 |
| `--deny` | `bool` | `false` | 拒绝请求 |
| `-h, --help` | `bool` | `false` | respond 帮助 |
| `--option` | `string` | `""` | 显式 provider 选项 ID（可选） |
| `--permission` | `string` | `""` | 要响应的权限 ID（来自升级事件） |
| `--reason` | `string` | `""` | 记录到诊断中的原因 |

## `amux task show`

显示任务状态和结果

**用法**

```text
amux task show <task-id> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h, --help` | `bool` | `false` | show 帮助 |

## `amux task submit`

提交子智能体任务

**用法**

```text
amux task submit <task prompt> [flags]
```

**选项**

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--agent` | `string` | `builtin/codex` | 要运行的 Agent Profile 引用或旧版 Agent 类型 |
| `--arg` | `stringArray` | `[]` | 附加智能体 CLI 参数（可重复） |
| `--commit` | `string` | `""` | 创建 git-worktree 的基准 Git commit/ref（默认 HEAD） |
| `--credential-profile` | `string` | `""` | 凭据 Profile 名称（由控制面解析） |
| `-h, --help` | `bool` | `false` | submit 帮助 |
| `--isolation` | `string` | `""` | 隔离模式：process \| private-home（默认 process） |
| `--model` | `string` | `""` | 执行模型引用；v2 专家省略时使用 Runtime 默认模型，旧版专家仍遵守其原有约束 |
| `--permission` | `string` | `""` | 权限策略：policy-allow \| escalate-to-main \| deny-all（默认 policy-allow） |
| `--profile` | `string` | `""` | 调度前解析的 Agent Profile 引用（覆盖 --agent） |
| `--repo-dir` | `string` | `""` | git-worktree 工作区的仓库缓存目录 |
| `--run-as` | `string` | `""` | 以已注册的 Principal 别名运行（由 amuxd 解析） |
| `--runtime` | `string` | `""` | 执行 Runtime 适配器；v2 专家必填 |
| `--timeout` | `int` | `0` | 任务超时秒数（0 = Runtime 默认值） |
| `--trusted-mode-ack` | `bool` | `true` | 旧版空操作：弱隔离下默认注入长期密钥（v0.38） |
| `--wait` | `bool` | `false` | 阻塞至任务结束并打印结果 |
| `--workspace` | `string` | `""` | 工作区策略：temp-dir \| git-worktree（默认 temp-dir） |

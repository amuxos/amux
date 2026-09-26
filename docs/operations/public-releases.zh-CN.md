# 公共二进制发行

[English](public-releases.md) | [简体中文](public-releases.zh-CN.md)

应用源码仓可以保持私有，公共产物发布在 `amuxos/amux-releases`，下载无需 GitHub
登录。下载仓保存说明和发行附件，不保存应用源码历史。

## 维护者配置

启用源码仓的 GitHub Actions。使用发布账号创建仅能访问 `amuxos/amux-releases`
的 fine-grained token，授予仓库 Contents 读写权限并设置有效期，将其存为源码仓
Actions Secret `AMUX_RELEASE_TOKEN`。到期前更新该 Secret。不要复用开发者 CLI
所用的宽权限 token。

工作流对源码仅有读取权限；只有发布步骤获得跨仓 Secret。下载仓自身不需要工作流。

## 发布

源码改动 review、合入后，使用 CI 工具链通过完整门禁，在验证过的 main 提交上创建
并推送稳定版本 `vX.Y.Z` tag。`.github/workflows/release.yml` 验证 tag 属于 main，
重新运行门禁，构建四个平台原生产物并校验完整性。所有附件上传至草稿 Release
完成后，才将其发布为 latest。

公开 tag 指向下载仓的说明文档分支，不指向或导入应用的私有 Git 历史。版本说明
独立编写，不自动复制私有提交信息。

也可手工触发工作流并指定已有稳定 tag。失败的草稿可以重试；已公开版本保持不变，
修正需要发布新版本。并发控制使发行任务串行执行。

## 下载约定

每个版本包含 `install.sh`、`version.txt`、`latest.json`、四个平台压缩包及其
`.sha256` 文件。安装器只解析一次 `version.txt`，随后下载固定版本的平台包和校验值。
`amux update --restart` 和更新检查使用 `latest.json`；清单路径下更新器先校验 SHA256
再解包（自定义 `--release-url`/`AMUX_RELEASE_URL` 直接指向 `.tar.gz` 归档时跳过校验）。
两条路径均拒绝不支持的归档条目。包内含许可证和适配平台的 Claude SDK sidecar。
安装与更新还会在未设置 `AGENT_PACK_CONTRACT_SCM_URL` 时，从
`amuxos/agent-pack-contract-releases` 下载托管的 Agent Pack Contract SCM 发布包，
并安装到 `~/.amux/libexec/agent-pack-contract`。原生 Agent 和 Node 需要另外安装。
特权隔离 helper 使用独立安装流程。

镜像可通过 `AMUX_RELEASE_BASE_URL` 指定安装器使用的 GitHub 风格 releases 目录；
后续 CLI 和服务更新检查使用 `AMUX_RELEASE_URL`（`AMUX_SCM_LATEST_URL` 作为旧别名
仍被接受）。环境变量覆盖不会自动持久化进原生服务环境。本地二进制安装及源码安装仍可用。

版本回退不等于数据库回退。文件替换阶段失败会恢复原二进制及 sidecar；替换之后的
错误需要先诊断，再决定是否能用旧版本读取当前用户数据。

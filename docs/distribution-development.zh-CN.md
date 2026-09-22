# 公共核心与独立发行版

[English](distribution-development.md) | [简体中文](distribution-development.zh-CN.md)

这个 module 是公共核心的唯一源码来源，可独立构建和测试。它默认包含 Codex、Claude、Pi，
以及公共 Profile、CLI、Hub、Runtime、Task、Session、Timeline 和飞书官方接入。

公共仓库地址为 <https://github.com/amuxos/amux.git>，Go module 为
`github.com/amuxos/amux`，官网为 <https://amuxos.com/>。

公共核心采用 [Apache-2.0](../LICENSE)，允许商业和闭源发行版；再分发时应保留适用的
许可证及归属声明。公共打包器在 `licenses/amux/` 中附带核心声明，构建其它发行版时
也采用该位置，但不因此为该发行版自己的代码授予许可。详见[许可与归属标识](licensing.zh-CN.md)
和[第三方声明](../THIRD_PARTY_NOTICES.zh-CN.md)。

其它发行版建立独立 Go module，依赖本 module，通过 `pkg/extensions` 注册附加能力，
再在自己的薄 `cmd` 入口调用 `app/cli.Main`、`app/hub.Main` 或 `app/runtime.Main`。
应用启动时关闭注册窗口；所有注册必须先完成。公共包不依赖任何发行版 module。

```text
发行版 main → 注册扩展 → 公共 app 入口 → 公共运行时
                  └─ Adapter / Provider / Profile / Bridge / 配置
```

通用功能只在这里修改。发行版拥有自己的 Provider、Profile 资源、配置默认值、发布源
和品牌；单仓开发时通过相对 `replace` 使用公共源码，独立发布后通过依赖版本升级同步。
不维护分支补丁、源文件覆盖或导出副本。
`cmd` 路径保持为标准构建入口，具体实现和测试位于 `app`。

Pi 的通用文件布局与扩展渲染由核心维护。发行版通过 Profile customization 的
`ValidateProvider` 和 `PiProviderConfig` 注册额外平台的校验、连接地址、API 与模型
兼容配置；默认 `openai-compat` 路径仍由公共核心处理。CLI customization 的
`CredentialSetExamples` 可以追加发行版凭据示例，示例使用传入的命令路径。
`SetupCommands` hook 可返回新建的 Cobra 命令，在 `amux setup` 下提供可选本地组件
安装入口。注册不能执行安装；仅用户显式执行命令时才允许下载或安装组件。

本地联调可在发行版的 `go.mod` 使用相对 `replace` 或不提交的 `go.work`。
公共版本发布后，发行版应锁定 `github.com/amuxos/amux` 的已发布版本，再移除本地
replace。联调时使用的 `v0.0.0` 只是占位版本，不能作为已发布版本从网络解析。
`make check` 独立验证公共核心。发行版另外运行自己的定制测试和组合测试。

接口遵守 Go 的 `internal` 边界。Facade 只公开扩展所需的契约，Session 和 Timeline
的实现及所有权仍在核心内部。Provider 复用同一个 canonical event 路径，不另外维护
Session、turn completion 或 transport 实现。这个接口尚未发布稳定性承诺。

## 与发行版协同修改

本 GitHub 仓库维护公共实现、接口、测试与双语文档。发行版固定经过 review 的公共
提交或版本；可以通过 submodule 和相对 Go `replace` 联调，也可以配置独立 checkout。

跨仓修改时，在两个 checkout 分别创建特性分支，并验证公共核心与发行版的真实装配。
先提交并合入公共 PR，再将发行版更新到实际合入的公共提交，验证组合后合入依赖更新。
两个仓库分别记录 commit；在父仓暂存 submodule 路径只记录其提交，不会保存公共
checkout 中未提交的文件。CI 和发行使用记录的版本，不自动跟随移动分支。

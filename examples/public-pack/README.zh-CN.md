# manifest v1.1 Pack 示例

[English](README.md) | [简体中文](README.zh-CN.md)

本目录包含 `dist/agent-pack.manifest.json` 和 `instructions/reviewer.md`，展示代码审查
Profile 的指令、模型与 Provider 绑定。运行时读取 manifest，Profile TOML 不参与运行时解析。

`https://provider.example/v1`、模型名和 `EXAMPLE_API_KEY` 都是示例值，不能直接调用模型。
使用前把 manifest 改为你的实际 Provider；或者移除示例 Provider 绑定并使用原生 Agent 配置。
凭据通过 [Credential Profile](../03-credential-profiles/README.zh-CN.md) 管理，不写进 Pack。

将本目录内容放在一个独立 Git 仓库的根目录并提交，然后注册：

```sh
amux agent pack add example /absolute/path/to/example-pack-repo
amux agent show example/reviewer --json
```

`pack add` 的地址是 Pack 自身的 Git 仓库，不能把当前父仓当作这个 Pack 的根目录。
源码格式见 [Agent Profile Pack](../../docs/reference/agent-profile-format.zh-CN.md)。

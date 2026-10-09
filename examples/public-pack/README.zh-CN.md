# manifest v2 Pack 示例

[English](README.md) | [简体中文](README.zh-CN.md)

本 Pack 包含代码审查指令和 v2 manifest，不声明引擎、模型、Provider 或权限设置。
这些设置在执行时选择。

将本目录内容复制到独立 Git 仓库，提交后注册：

```sh
amux agent pack add example /absolute/path/to/example-pack-repo
amux agent show example/reviewer --json
amux session run example/reviewer --runtime codex
```

`pack add` 指向 Pack 自己的 Git 仓库，不能使用父仓库根目录。
在 Runtime 配置所选引擎的凭据。参见 [Pack 格式](../../docs/reference/agent-profile-format.zh-CN.md)。

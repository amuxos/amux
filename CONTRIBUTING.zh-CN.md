# 参与贡献

[English](CONTRIBUTING.md) | [简体中文](CONTRIBUTING.zh-CN.md)

公共核心的修改提交至 <https://github.com/amuxos/amux>。工程与验证规则见
[AGENTS.md](AGENTS.zh-CN.md)。英文和简体中文文档应同步维护。

## 贡献许可

按照 [Apache-2.0](LICENSE) 第 5 条，除明确另有声明外，有意提交并纳入 amux 的贡献
采用 Apache-2.0。贡献者必须有权按这些条款提交相关材料，包括取得雇主或其它权利人
必要的授权。贡献不意味着转让版权归属。

保留已有作者、许可证和归属声明。在变更中注明第三方材料及其原始许可证，
不要将他人的代码标为 amux 所有，也不要擅自更改其许可。详见[第三方声明](THIRD_PARTY_NOTICES.zh-CN.md)。

Go 依赖变化时，运行 `python3 scripts/update-go-notices.py`，将生成的声明集合与依赖
更新一起 review。使用 `--check` 可只检查、不重写文件。完整门禁执行该检查，因此除
AGENTS.md 说明的工具外，还需要 Python 3。Claude sidecar 依赖变化时，还应核对其
锁文件和适用条款。

许可与集成示例见[许可说明](docs/licensing.zh-CN.md)。

## 文档工作流

公共文档以飞书用户路径为主线。修改对话身份、绑定或 Turn/Task 边界前，阅读
[Session 与对话模型](docs/current/session-product-model.zh-CN.md)。新增页面时维护
显式双语目录 `docs/navigation.json`。`make docs` 还会从真实帮助注册表生成飞书命令参考
和仓库索引，`make docs-check` 检查其漂移与目录。读者分层、状态标记、检查范围及网站
重新生成流程见[文档维护指南](docs/maintainers/documentation-system.zh-CN.md)。

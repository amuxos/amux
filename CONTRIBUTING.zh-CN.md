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

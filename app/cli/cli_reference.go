package cli

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const cliReferenceRuntimeDefaultAnnotation = "amux.io/cli-reference-runtime-default"

const (
	allCLIPlatforms                 = "all"
	cliReferencePlatformsAnnotation = "amux.io/cli-reference-platforms"
)

func markCommandPlatforms(cmd *cobra.Command, platforms ...string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[cliReferencePlatformsAnnotation] = strings.Join(platforms, ",")
}

// markFlagDefaultRuntimeDependent keeps generated documentation deterministic
// when a Cobra default is derived from the current host. The live help output
// still shows the resolved value; the checked-in reference labels the value as
// runtime-dependent instead of publishing one developer machine's value.
func markFlagDefaultRuntimeDependent(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			panic(fmt.Sprintf("mark runtime-dependent default for unknown flag %q", name))
		}
		if flag.Annotations == nil {
			flag.Annotations = map[string][]string{}
		}
		flag.Annotations[cliReferenceRuntimeDefaultAnnotation] = []string{"true"}
	}
}

func renderCLIReference(root *cobra.Command) []byte {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	commands := publicCommands(root)
	for _, cmd := range commands {
		cmd.InitDefaultHelpFlag()
		cmd.InitDefaultVersionFlag()
	}

	var out bytes.Buffer
	out.WriteString("<!-- Code generated from app/cli's Cobra command tree; DO NOT EDIT. -->\n\n")
	out.WriteString("# amux CLI 命令参考\n\n")
	out.WriteString("本文档由当前 `amux` Cobra 命令树自动生成，覆盖全部支持平台上的公开命令、参数、选项、默认值、别名和源码中登记的示例。根命令选项是所有子命令可用的全局选项；平台受限命令会明确标注，隐藏命令与隐藏选项不会被收录，也不构成公开 CLI 契约。\n\n")
	out.WriteString("修改公开命令的名称、层级、可见性、说明、参数、选项或默认值后，必须运行 `make cli-doc`；仓库统一门禁会拒绝生成结果不一致的改动。产品语义与行为边界仍以 [`docs/current/cli-product-model.md`](../current/cli-product-model.md) 为准。\n\n")
	out.WriteString("## 命令索引\n\n")
	out.WriteString("| 命令 | 说明 |\n")
	out.WriteString("| --- | --- |\n")
	for _, cmd := range commands {
		fmt.Fprintf(&out, "| %s | %s |\n", markdownCode(cmd.CommandPath()), escapeTable(cmd.Short))
	}

	for _, cmd := range commands {
		renderCommandReference(&out, cmd)
	}
	return out.Bytes()
}

func publicCommands(root *cobra.Command) []*cobra.Command {
	commands := make([]*cobra.Command, 0)
	var walk func(*cobra.Command, bool)
	walk = func(cmd *cobra.Command, parentVisible bool) {
		visible := parentVisible && !cmd.Hidden
		if !visible {
			return
		}
		commands = append(commands, cmd)
		for _, child := range sortedPublicChildren(cmd) {
			walk(child, true)
		}
	}
	walk(root, true)
	return commands
}

func sortedPublicChildren(cmd *cobra.Command) []*cobra.Command {
	children := make([]*cobra.Command, 0, len(cmd.Commands()))
	for _, child := range cmd.Commands() {
		if !child.Hidden {
			children = append(children, child)
		}
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name() < children[j].Name()
	})
	return children
}

func renderCommandReference(out *bytes.Buffer, cmd *cobra.Command) {
	fmt.Fprintf(out, "\n## %s\n\n", markdownCode(cmd.CommandPath()))
	if cmd.Short != "" {
		fmt.Fprintf(out, "%s\n\n", strings.TrimSpace(cmd.Short))
	}
	if platforms := effectiveCommandPlatforms(cmd); len(platforms) > 0 {
		values := make([]string, 0, len(platforms))
		for _, platform := range platforms {
			values = append(values, markdownCode(platform))
		}
		fmt.Fprintf(out, "**适用平台：** %s\n\n", strings.Join(values, "、"))
	}
	if long := strings.TrimSpace(cmd.Long); long != "" && long != strings.TrimSpace(cmd.Short) {
		fmt.Fprintf(out, "%s\n\n", long)
	}

	out.WriteString("**用法**\n\n```text\n")
	out.WriteString(strings.TrimSpace(cmd.UseLine()))
	out.WriteString("\n```\n")

	if len(cmd.Aliases) > 0 {
		aliases := make([]string, 0, len(cmd.Aliases))
		for _, alias := range cmd.Aliases {
			aliases = append(aliases, markdownCode(alias))
		}
		fmt.Fprintf(out, "\n**别名：** %s\n", strings.Join(aliases, "、"))
	}
	if example := strings.TrimSpace(cmd.Example); example != "" {
		out.WriteString("\n**示例**\n\n```bash\n")
		out.WriteString(example)
		out.WriteString("\n```\n")
	}

	children := sortedPublicChildren(cmd)
	if len(children) > 0 {
		out.WriteString("\n**子命令**\n\n")
		out.WriteString("| 命令 | 说明 |\n")
		out.WriteString("| --- | --- |\n")
		for _, child := range children {
			fmt.Fprintf(out, "| %s | %s |\n", markdownCode(child.CommandPath()), escapeTable(child.Short))
		}
	}

	flags := visibleNonInheritedFlags(cmd)
	if len(flags) > 0 {
		out.WriteString("\n**选项**\n\n")
		out.WriteString("| 选项 | 类型 | 默认值 | 说明 |\n")
		out.WriteString("| --- | --- | --- | --- |\n")
		for _, flag := range flags {
			option := "--" + flag.Name
			if flag.Shorthand != "" {
				option = "-" + flag.Shorthand + ", " + option
			}
			usage := flag.Usage
			if _, required := flag.Annotations[cobra.BashCompOneRequiredFlag]; required {
				usage += " (required)"
			}
			if flag.Deprecated != "" {
				usage += " (deprecated: " + flag.Deprecated + ")"
			}
			defaultValue := flag.DefValue
			if _, dynamic := flag.Annotations[cliReferenceRuntimeDefaultAnnotation]; dynamic {
				defaultValue = "runtime-dependent"
			}
			if defaultValue == "" {
				defaultValue = `""`
			}
			fmt.Fprintf(out, "| %s | %s | %s | %s |\n",
				markdownCode(option),
				markdownCode(flag.Value.Type()),
				markdownCode(defaultValue),
				escapeTable(usage),
			)
		}
	}
}

func effectiveCommandPlatforms(cmd *cobra.Command) []string {
	for current := cmd; current != nil; current = current.Parent() {
		value := strings.TrimSpace(current.Annotations[cliReferencePlatformsAnnotation])
		if value != "" {
			return strings.Split(value, ",")
		}
	}
	return nil
}

func visibleNonInheritedFlags(cmd *cobra.Command) []*pflag.Flag {
	flags := make([]*pflag.Flag, 0)
	cmd.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if !flag.Hidden {
			flags = append(flags, flag)
		}
	})
	return flags
}

func markdownCode(value string) string {
	if strings.Contains(value, "`") {
		return "``" + value + "``"
	}
	return "`" + value + "`"
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "|", "\\|")
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}

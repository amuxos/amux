package config

const defaultWorkspaceProfileRef = "builtin/codex"

func distributionConfigOptions(shared []ConfigOption) []ConfigOption {
	return append(shared, []ConfigOption{
		{Section: "Structured Provider", Key: "provider.structured.<provider>.turn_idle_timeout_seconds", Type: "integer seconds", Default: "继承全局", Constraints: "provider 为 codex；0 禁用，负数回 provider 默认，最大 604800", Description: "指定 provider 的 turn idle 覆盖。"},
		{Section: "Structured Provider", Key: "provider.structured.<provider>.turn_hard_cap_seconds", Type: "integer seconds", Default: "继承全局", Constraints: "provider 为 codex", Description: "指定 provider 的 turn hard cap 覆盖。"},
		{Section: "Structured Provider", Key: "provider.structured.<provider>.tool_idle_timeout_seconds", Type: "integer seconds", Default: "继承全局", Constraints: "provider 为 codex", Description: "指定 provider 的 tool idle 覆盖。"},
		{Section: "Structured Provider", Key: "provider.structured.<provider>.repeat_tool_call_limit", Type: "integer count", Default: "继承全局", Constraints: "provider 为 codex；0 禁用，负数回 provider 默认，最大 1000", Description: "指定 provider 的连续相同工具调用次数上限覆盖。"},
	}...)
}

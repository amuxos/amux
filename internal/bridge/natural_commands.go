package bridge

import "strings"

func parseNaturalWorkspaceDefaultAgent(text string) (string, bool) {
	text = normalizeNaturalCommandText(text)
	const prefix = "这个群默认用 "
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	agent := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if agent == "" || strings.Contains(agent, " ") {
		return "", false
	}
	return agent, true
}

func parseNaturalProfileDefaultAgent(text string) (string, bool) {
	text = normalizeNaturalCommandText(text)
	for _, prefix := range []string{"以后默认用 ", "默认用 "} {
		if strings.HasPrefix(text, prefix) {
			agent := strings.TrimSpace(strings.TrimPrefix(text, prefix))
			if agent == "" || strings.Contains(agent, " ") {
				return "", false
			}
			return agent, true
		}
	}
	return "", false
}

func normalizeNaturalCommandText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

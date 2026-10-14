package command

import (
	"errors"
	"strconv"
	"strings"
)

type GoalCommand struct {
	Action      string
	Objective   string
	TokenBudget *int64
}

// ParseGoal recognizes only a complete leading bare word. It preserves the
// objective's whitespace and never recognizes /goal or mentions in prose.
func ParseGoal(text string) (GoalCommand, bool, error) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "goal") {
		return GoalCommand{}, false, nil
	}
	rest := strings.TrimSpace(text[len(fields[0]):])
	if rest == "" {
		return GoalCommand{Action: "get"}, true, nil
	}
	parts := strings.Fields(rest)
	action := strings.ToLower(parts[0])
	switch action {
	case "pause", "resume", "clear":
		if len(parts) != 1 {
			return GoalCommand{}, true, errors.New("goal control takes no additional arguments")
		}
		return GoalCommand{Action: action}, true, nil
	case "budget":
		if len(parts) != 2 {
			return GoalCommand{}, true, errors.New("goal budget requires one positive integer")
		}
		budget, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || budget <= 0 {
			return GoalCommand{}, true, errors.New("goal budget requires one positive integer")
		}
		return GoalCommand{Action: action, TokenBudget: &budget}, true, nil
	case "set":
		objective := strings.TrimSpace(rest[len(parts[0]):])
		if objective == "" {
			return GoalCommand{}, true, errors.New("goal set requires an objective")
		}
		return GoalCommand{Action: action, Objective: objective}, true, nil
	default:
		return GoalCommand{Action: "create", Objective: rest}, true, nil
	}
}

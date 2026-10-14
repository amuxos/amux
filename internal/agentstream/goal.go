package agentstream

// GoalState mirrors native persisted goal state. The provider owns lifecycle
// and usage accounting; amux stores observations, not a second goal scheduler.
type GoalState struct {
	ThreadID        string `json:"threadId"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget,omitempty"`
	TokensUsed      int64  `json:"tokensUsed"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
}

func (g *GoalState) Clone() *GoalState {
	if g == nil {
		return nil
	}
	copy := *g
	if g.TokenBudget != nil {
		budget := *g.TokenBudget
		copy.TokenBudget = &budget
	}
	return &copy
}

func (g *GoalState) IsTerminal() bool {
	return g != nil && g.Status == "complete"
}

// GoalChanged is a complete sticky snapshot, including an empty goal after
// clear. Unsupported providers do not advertise a goal command.
type GoalChanged struct {
	Supported bool       `json:"supported"`
	Goal      *GoalState `json:"goal,omitempty"`
}

func (GoalChanged) Kind() Kind        { return KindGoalChanged }
func (GoalChanged) eventBody()        {}
func (GoalChanged) sessionPayload()   {}
func (g GoalChanged) cloneBody() Body { g.Goal = g.Goal.Clone(); return g }

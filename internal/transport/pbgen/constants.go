package pbgen

const (
	TaskMethodSubmit  = "TaskSubmit"
	TaskMethodGet     = "TaskGet"
	TaskMethodList    = "TaskList"
	TaskMethodPrune   = "TaskPrune"
	TaskMethodCancel  = "TaskCancel"
	TaskMethodRespond = "TaskRespond"
	TaskMethodStream  = "TaskStream"
	// ResolveParentTurn stays on the task control plane: amuxd must not route a
	// task provenance lookup through the ordinary child-session data plane.
	TaskMethodResolveParentTurn = "TaskResolveParentTurn"
	// Agent-instance (Phase 3) methods, tunneled from amuxd to the amuxlet
	// hosting the instance's sticky target. ListAgentInstances / GetAgentInstance
	// are answered by amuxd from its durable store and do not use these constants.
	TaskMethodInstanceSend      = "TaskInstanceSend"
	TaskMethodInstanceInterrupt = "TaskInstanceInterrupt"
	TaskMethodInstanceWait      = "TaskInstanceWait"
	TaskMethodInstanceDescribe  = "TaskInstanceDescribe"
	TaskMethodInstanceList      = "TaskInstanceList"
	TaskMethodInstanceGet       = "TaskInstanceGet"
	TaskMethodInstanceCleanup   = "TaskInstanceCleanup"
)

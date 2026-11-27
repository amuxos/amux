package extensions

import "github.com/amuxos/amux/internal/feishumsg"

type FeishuMessage = feishumsg.Message
type PersonalFeishuMessageResult = feishumsg.PersonalResult
type RunPersonalFeishuCommand = feishumsg.RunPersonalCommand
type RunPersonalFeishuEventCommand = feishumsg.RunPersonalEventCommand
type PersonalScheduleIdentity = feishumsg.PersonalScheduleIdentity
type PersonalScheduleAccount = feishumsg.PersonalScheduleAccount
type PersonalScheduleEvent = feishumsg.PersonalScheduleEvent
type PersonalScheduleCard = feishumsg.PersonalScheduleCard

var (
	ErrPersonalFeishuLogin = feishumsg.ErrPersonalLogin
	ErrPersonalFeishuBot   = feishumsg.ErrPersonalBot
	ErrEmptyFeishuMessage  = feishumsg.ErrEmptyResponse
)

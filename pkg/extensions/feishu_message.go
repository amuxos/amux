package extensions

import "github.com/amuxos/amux/internal/feishumsg"

type FeishuMessage = feishumsg.Message
type PersonalFeishuMessageResult = feishumsg.PersonalResult
type RunPersonalFeishuCommand = feishumsg.RunPersonalCommand

var (
	ErrPersonalFeishuLogin = feishumsg.ErrPersonalLogin
	ErrPersonalFeishuBot   = feishumsg.ErrPersonalBot
	ErrEmptyFeishuMessage  = feishumsg.ErrEmptyResponse
)

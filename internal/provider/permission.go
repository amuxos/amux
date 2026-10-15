package provider

import "encoding/json"

type Permission struct {
	ID       string              `json:"id"`
	Provider string              `json:"provider,omitempty"`
	ToolCall *PermissionToolCall `json:"toolCall,omitempty"`
	Options  []PermissionOption  `json:"options,omitempty"`
	Raw      json.RawMessage     `json:"raw,omitempty"`
}

type PermissionToolCall struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Title    string          `json:"title,omitempty"`
	RawInput json.RawMessage `json:"rawInput,omitempty"`
	// Question carries the prompt text for a user-input permission
	// (item/tool/requestUserInput). Empty for ordinary approvals.
	Question string `json:"question,omitempty"`
	// MultiSelect indicates the user may select multiple options. Only set for
	// user-input permissions.
	MultiSelect bool  `json:"multiSelect,omitempty"`
	AllowOther  bool  `json:"allowOther,omitempty"`
	Secret      bool  `json:"secret,omitempty"`
	Blocking    *bool `json:"blocking,omitempty"`
}

type PermissionOption struct {
	OptionId    string `json:"optionId"`
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
}

type PermissionResponse struct {
	PermissionId string   `json:"permission_id"`
	OptionId     string   `json:"option_id"`
	OptionIds    []string `json:"option_ids,omitempty"`
	Cancel       bool     `json:"cancel,omitempty"`
	Message      string   `json:"message,omitempty"`
}

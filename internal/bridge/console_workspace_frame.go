package bridge

import (
	"encoding/json"
	"strconv"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The wire card contains display data and indices only. Fixed callback values,
// opaque confirmations and allowed form fields stay with the exact frame.
type browserCardFrame struct {
	card     json.RawMessage
	controls map[string]browserCardControl
}
type browserCardControl struct {
	tag, name string
	values    map[string]any
	field     map[string]any
	form      map[string]map[string]any
}

func browserFrame(raw string) browserCardFrame {
	f := browserCardFrame{controls: map[string]browserCardControl{}}
	var doc map[string]any
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return f
	}
	var walk func(map[string]any, map[string]map[string]any)
	walk = func(n map[string]any, form map[string]map[string]any) {
		tag, _ := n["tag"].(string)
		if tag == "form" {
			form = map[string]map[string]any{}
		}
		name, _ := n["name"].(string)
		if form != nil && name != "" && (tag == "input" || tag == "select_static") {
			form[name] = n
		}
		values, _ := n["value"].(map[string]any)
		if behaviors, ok := n["behaviors"].([]any); ok {
			for _, item := range behaviors {
				b, _ := item.(map[string]any)
				if b["type"] == "callback" {
					values, _ = b["value"].(map[string]any)
				} else if b["type"] == "open_url" {
					n["url"] = b["default_url"]
				}
			}
		}
		delete(n, "behaviors")
		if tag == "button" || tag == "select_static" || tag == "interactive_container" {
			delete(n, "value")
			if len(values) > 0 && n["disabled"] != true {
				id := strconv.Itoa(len(f.controls) + 1)
				n["control"] = id
				f.controls[id] = browserCardControl{tag: tag, name: name, values: values, field: n, form: form}
			}
		}
		for _, key := range []string{"elements", "columns", "actions"} {
			nodes, ok := n[key].([]any)
			if !ok {
				continue
			}
			kept := nodes[:0]
			for _, item := range nodes {
				child, ok := item.(map[string]any)
				if !ok || child["element_id"] == "amux_private_console_password" {
					continue
				}
				walk(child, form)
				kept = append(kept, child)
			}
			n[key] = kept
		}
	}
	body, _ := doc["body"].(map[string]any)
	walk(body, nil)
	f.card, _ = json.Marshal(doc)
	return f
}
func validBrowserField(field map[string]any, value string) bool {
	if field == nil || field["disabled"] == true {
		return false
	}
	if field["tag"] == "input" {
		return len(value) <= 32768
	}
	options, _ := field["options"].([]any)
	for _, item := range options {
		option, _ := item.(map[string]any)
		if option["value"] == value {
			return true
		}
	}
	return false
}
func (f browserCardFrame) action(in ConsoleWorkspaceRequest) (*callback.CallBackAction, error) {
	invalid := status.Error(codes.InvalidArgument, "console_invalid_card_control")
	control, ok := f.controls[in.Control]
	if !ok {
		return nil, invalid
	}
	if control.tag == "select_static" {
		if !validBrowserField(control.field, in.Option) {
			return nil, invalid
		}
	} else if in.Option != "" {
		return nil, invalid
	}
	fields := map[string]any{}
	for name, value := range in.Fields {
		if !validBrowserField(control.form[name], value) {
			return nil, invalid
		}
		fields[name] = value
	}
	if control.field["action_type"] == "form_submit" {
		for name, field := range control.form {
			if field["required"] == true && field["disabled"] != true && in.Fields[name] == "" {
				return nil, invalid
			}
		}
	}
	return &callback.CallBackAction{Tag: control.tag, Name: control.name, Value: control.values, Option: in.Option, FormValue: fields}, nil
}

package bridge

import (
	"context"
	"testing"
)

func TestEventDispatcherAcknowledgesAppReactionEcho(t *testing.T) {
	payload := []byte(`{
		"schema": "2.0",
		"header": {
			"event_id": "evt_reaction_echo",
			"event_type": "im.message.reaction.created_v1"
		},
		"event": {
			"app_id": "cli_test",
			"message_id": "om_test",
			"operator_type": "app",
			"reaction_type": {"emoji_type": "OneSecond"}
		}
	}`)

	if _, err := (&Bridge{}).newEventDispatcher().Do(context.Background(), payload); err != nil {
		t.Fatalf("reaction echo must be acknowledged without redelivery: %v", err)
	}
}

func TestEventDispatcherAcknowledgesDocumentCommentsWhileSourceDisabled(t *testing.T) {
	payload := []byte(`{
		"schema": "2.0",
		"header": {
			"event_id": "evt_comment_disabled",
			"event_type": "drive.notice.comment_add_v1",
			"create_time": "1785292800000"
		},
		"event": {
			"comment_id": "comment-1",
			"file_token": "doc-1",
			"file_type": "docx",
			"create_time": "1785292800000",
			"user_id": {"open_id": "owner-open"}
		}
	}`)

	if _, err := (&Bridge{}).newEventDispatcher().Do(context.Background(), payload); err != nil {
		t.Fatalf("disabled document comment event must be acknowledged without processing: %v", err)
	}
}

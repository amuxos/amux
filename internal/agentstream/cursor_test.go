package agentstream_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestCursorTokenRoundTripAndRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	want := agentstream.Cursor{Epoch: "epoch-1", Seq: 42}
	token, err := agentstream.EncodeCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || token == "epoch-1:42" {
		t.Fatalf("cursor token = %q, want non-empty opaque value", token)
	}
	got, err := agentstream.DecodeCursor(token)
	if err != nil || got != want {
		t.Fatalf("DecodeCursor() = (%#v, %v), want %#v", got, err, want)
	}
	zero, err := agentstream.DecodeCursor("")
	if err != nil || zero != (agentstream.Cursor{}) {
		t.Fatalf("DecodeCursor(empty) = (%#v, %v)", zero, err)
	}
	for _, invalid := range []string{"not-base64!", "e30", token + "tampered"} {
		if _, err := agentstream.DecodeCursor(invalid); !errors.Is(err, agentstream.ErrInvalidCursor) {
			t.Fatalf("DecodeCursor(%q) error = %v, want ErrInvalidCursor", invalid, err)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["future"] = true
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := agentstream.DecodeCursor(unknownField); !errors.Is(err, agentstream.ErrInvalidCursor) {
		t.Fatalf("DecodeCursor(unknown field) error = %v, want ErrInvalidCursor", err)
	}
}

package isolation

import (
	"bytes"
	"reflect"
	"testing"
)

func TestHelperProtocolFrameRoundTrip(t *testing.T) {
	want := HelperRequest{
		ProtocolVersion: HelperProtocolVersion,
		RequestID:       "request-1",
		Operation:       OpSignal,
		LaunchID:        "launch-1",
		Signal:          "TERM",
		HomeCaches: []HomeCacheSpec{{
			Path: ".trae/cli/logs_2.sqlite", MaxBytes: 1 << 30,
			Remove: []string{".trae/cli/logs_2.sqlite"},
		}},
	}
	frame, err := encodeHelperFrame(want)
	if err != nil {
		t.Fatalf("encodeHelperFrame: %v", err)
	}
	var got HelperRequest
	if err := decodeHelperFrame(frame, &got); err != nil {
		t.Fatalf("decodeHelperFrame: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
}

func TestValidateLaunchSpecRejectsNULAndOversize(t *testing.T) {
	if err := validateLaunchSpec(&LaunchSpec{Path: "/bin/sh", Args: []string{"bad\x00arg"}}); err == nil {
		t.Fatal("expected NUL rejection")
	}
	if err := validateLaunchSpec(&LaunchSpec{Path: "/bin/sh", Env: []string{"X=" + string(bytes.Repeat([]byte{'x'}, MaxLaunchVectorBytes))}}); err == nil {
		t.Fatal("expected oversized vector rejection")
	}
}

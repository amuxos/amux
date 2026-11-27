package sessionfiles

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHelperUpgradeDiagnosticSurvivesTunnel(t *testing.T) {
	cause := fmt.Errorf("%w: unsupported helper operation", ErrHelperUpgradeRequired)
	for _, err := range []error{cause, errors.New(status.Error(codes.Unimplemented, cause.Error()).Error())} {
		if !NeedsHelperUpgrade(err) {
			t.Fatalf("lost upgrade diagnostic: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("permission denied"), fmt.Errorf("open %s: not found", ErrHelperUpgradeRequired), errors.New("unsupported helper operation")} {
		if NeedsHelperUpgrade(err) {
			t.Fatalf("misclassified unrelated error: %v", err)
		}
	}
}

func TestArtifactTooLargeDiagnosticSurvivesTunnel(t *testing.T) {
	wire := status.Error(codes.ResourceExhausted, ErrArtifactTooLarge.Error())
	for _, err := range []error{ErrArtifactTooLarge, wire, errors.New(wire.Error())} {
		if !ArtifactTooLarge(err) {
			t.Fatalf("lost size diagnostic: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("permission denied"), fmt.Errorf("open %s: not found", ErrArtifactTooLarge)} {
		if ArtifactTooLarge(err) {
			t.Fatalf("misclassified unrelated error: %v", err)
		}
	}
}

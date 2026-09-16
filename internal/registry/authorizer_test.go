package registry

import "testing"

func TestAuthorizerOpenByDefault(t *testing.T) {
	a := NewAuthorizer(AuthorizerConfig{})
	if a.Enabled() {
		t.Fatal("empty authorizer should be disabled (open)")
	}
	if err := a.Authorize("any", "", Capabilities{}); err != nil {
		t.Fatalf("open authorizer should allow all, got %v", err)
	}
}

func TestAuthorizerSharedToken(t *testing.T) {
	a := NewAuthorizer(AuthorizerConfig{SharedToken: "s3cret"})
	if err := a.Authorize("t1", "s3cret", Capabilities{}); err != nil {
		t.Fatalf("correct token rejected: %v", err)
	}
	if err := a.Authorize("t1", "wrong", Capabilities{}); err != ErrBadToken {
		t.Fatalf("err = %v, want ErrBadToken", err)
	}
	if err := a.Authorize("t1", "", Capabilities{}); err != ErrBadToken {
		t.Fatalf("missing token err = %v, want ErrBadToken", err)
	}
}

func TestAuthorizerTargetAllowlist(t *testing.T) {
	a := NewAuthorizer(AuthorizerConfig{AllowedTargets: []string{"mac-mini-01"}})
	if err := a.Authorize("mac-mini-01", "", Capabilities{}); err != nil {
		t.Fatalf("allowed target rejected: %v", err)
	}
	if err := a.Authorize("rogue", "", Capabilities{}); err != ErrTargetNotAllowed {
		t.Fatalf("err = %v, want ErrTargetNotAllowed", err)
	}
}

func TestAuthorizerLabelAllowlist(t *testing.T) {
	a := NewAuthorizer(AuthorizerConfig{
		AllowedLabels: map[string]map[string]string{
			"t1": {"pool": "local-dev", "arch": "arm64"},
		},
	})
	ok := Capabilities{Labels: map[string]string{"pool": "local-dev"}}
	if err := a.Authorize("t1", "", ok); err != nil {
		t.Fatalf("in-allowlist label rejected: %v", err)
	}
	bad := Capabilities{Labels: map[string]string{"pool": "prod"}}
	if err := a.Authorize("t1", "", bad); err != ErrLabelNotAllowed {
		t.Fatalf("err = %v, want ErrLabelNotAllowed", err)
	}
	// A target without a label allowlist has no label restriction.
	if err := a.Authorize("other", "", bad); err != nil {
		t.Fatalf("unrestricted target should allow any label, got %v", err)
	}
}

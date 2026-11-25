package consoleentry

import (
	"context"
	"errors"
	"testing"
)

type stubOriginResolver struct {
	active bool
	url    string
	err    error
	calls  int
	port   int
}

func (s *stubOriginResolver) Active() bool { return s.active }
func (s *stubOriginResolver) Resolve(_ context.Context, port int) (string, error) {
	s.calls++
	s.port = port
	return s.url, s.err
}

func TestPublicOriginResolverRegistrationAndValidation(t *testing.T) {
	prev := registeredPublicOriginResolver
	t.Cleanup(func() { registeredPublicOriginResolver = prev })
	registeredPublicOriginResolver = nil

	if HasActivePublicOriginResolver() {
		t.Fatal("unregistered resolver reported active")
	}
	if got, err := ResolvePublicOrigin(context.Background(), 7892); err != nil || got != "" {
		t.Fatalf("unregistered resolve = %q %v", got, err)
	}
	if err := RegisterPublicOriginResolver(nil); err == nil {
		t.Fatal("nil resolver accepted")
	}

	stub := &stubOriginResolver{active: false, url: "https://console.example.com/"}
	if err := RegisterPublicOriginResolver(stub); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := RegisterPublicOriginResolver(stub); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if HasActivePublicOriginResolver() {
		t.Fatal("inactive resolver reported active")
	}
	if got, err := ResolvePublicOrigin(context.Background(), 7892); err != nil || got != "" || stub.calls != 0 {
		t.Fatalf("inactive resolve = %q %v calls=%d", got, err, stub.calls)
	}

	stub.active = true
	if !HasActivePublicOriginResolver() {
		t.Fatal("active resolver reported inactive")
	}
	got, err := ResolvePublicOrigin(context.Background(), 7892)
	if err != nil || got != "https://console.example.com" || stub.port != 7892 {
		t.Fatalf("valid resolve = %q %v port=%d", got, err, stub.port)
	}

	for _, bad := range []string{"http://127.0.0.1:7892", "https://console.example.com/?token=secret", "https://user:pass@console.example.com", "ftp://console.example.com"} {
		stub.url = bad
		if got, err := ResolvePublicOrigin(context.Background(), 7892); !errors.Is(err, ErrInvalidURL) || got != "" {
			t.Fatalf("bad URL %q = %q %v, want ErrInvalidURL", bad, got, err)
		}
	}

	wantErr := errors.New("resolver failed")
	stub.url = ""
	stub.err = wantErr
	if _, err := ResolvePublicOrigin(context.Background(), 7892); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

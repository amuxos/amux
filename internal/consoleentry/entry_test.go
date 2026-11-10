package consoleentry

import "testing"

func TestConsoleOriginValidation(t *testing.T) {
	for _, raw := range []string{"http://10.0.0.4:7892", "https://console.example.com", "http://127.0.0.1:7892/", "https://[2001:db8::1]:7892"} {
		if _, err := ValidateURL(raw); err != nil {
			t.Fatalf("valid origin %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "http://0.0.0.0:7892", "http://[::]:7892", "http://host:0", "http://host:65536", "https://user:secret@host", "http://host?token=x", "http://host#pair=secret", "http://host/path", "javascript:alert(1)", "http://host\\other", " http://host"} {
		if _, err := ValidateURL(raw); err == nil {
			t.Fatalf("unsafe origin %q", raw)
		}
	}
}

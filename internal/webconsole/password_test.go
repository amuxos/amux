package webconsole

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTemporaryPasswordLoginAndWindow(t *testing.T) {
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Minute)
	s, err := New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", Password: "test-temporary-password-32", ExpiresAt: deadline, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	metadata := request(s, "GET", "/auth/config", "", "", "", nil)
	if metadata.Code != 200 || !strings.Contains(metadata.Body.String(), `"mode":"password"`) || !strings.Contains(metadata.Body.String(), `"insecure_transport":true`) {
		t.Fatalf("metadata: %d %s", metadata.Code, metadata.Body)
	}
	if strings.Contains(metadata.Body.String(), "test-temporary-password") {
		t.Fatal("password metadata exposed")
	}
	wrong := request(s, "POST", "/auth/login", `{"password":"wrong"}`, "http://127.0.0.1:7892", "", nil)
	if wrong.Code != 401 {
		t.Fatal(wrong.Code)
	}
	body := `{"password":"test-temporary-password-32"}`
	cookies := make([]*http.Cookie, 0, 2)
	for i := 0; i < 2; i++ {
		w := request(s, "POST", "/auth/login", body, "http://127.0.0.1:7892", "", nil)
		if w.Code != 200 {
			t.Fatalf("repeat login: %d %s", w.Code, w.Body)
		}
		var value struct {
			Expires int64 `json:"expires_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Expires != deadline.Unix() {
			t.Fatal("login extended window")
		}
		cookies = append(cookies, w.Result().Cookies()[0])
	}
	if cookies[0].Value == cookies[1].Value {
		t.Fatal("browser sessions share token")
	}
	if w := request(s, "POST", "/auth/pair", `{"code":"test-temporary-password-32"}`, "http://127.0.0.1:7892", "", nil); w.Code != 404 {
		t.Fatal("password accepted as pair")
	}
	now = deadline.Add(time.Second)
	for _, cookie := range cookies {
		if w := request(s, "GET", "/api/console/bootstrap", "", "", "", cookie); w.Code != 401 || !strings.Contains(w.Body.String(), "console_expired") {
			t.Fatal("expired window accepted")
		}
	}
	if w := request(s, "POST", "/auth/login", body, "http://127.0.0.1:7892", "", nil); w.Code != 401 {
		t.Fatal("password survived expiry")
	}
}

func TestTemporaryPasswordCloseAndBruteForce(t *testing.T) {
	s, err := New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", Password: "test-temporary-password-32", ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		w := request(s, "POST", "/auth/login", `{"password":"wrong"}`, "http://127.0.0.1:7892", "", nil)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(s, "POST", "/auth/login", `{"password":"test-temporary-password-32"}`, "http://127.0.0.1:7892", "", nil); w.Code != 429 {
		t.Fatal("password not rate limited")
	}
	s.Close()
	if w := request(s, "GET", "/auth/config", "", "", "", nil); w.Code != 401 {
		t.Fatal("closed service metadata active")
	}
}
func TestTemporaryPasswordOrigin(t *testing.T) {
	s, err := New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", Password: "test-temporary-password-32", ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"", "http://attacker.example"} {
		if w := request(s, "POST", "/auth/login", `{"password":"test-temporary-password-32"}`, origin, "", nil); w.Code != 403 {
			t.Fatal("cross-origin login allowed")
		}
	}
}

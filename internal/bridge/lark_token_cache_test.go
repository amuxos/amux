package bridge

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

type fakeLarkHTTP struct {
	mu        sync.Mutex
	tokens    int
	apiAuth   []string
	rejectFor string
}

func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func (f *fakeLarkHTTP) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(req.URL.Path, "tenant_access_token") {
		f.tokens++
		tok := "t" + string(rune('0'+f.tokens))
		return jsonResp(`{"code":0,"tenant_access_token":"` + tok + `","expire":7200}`), nil
	}
	auth := req.Header.Get("Authorization")
	f.apiAuth = append(f.apiAuth, auth)
	if auth == "Bearer "+f.rejectFor {
		return jsonResp(`{"code":99991663,"msg":"Invalid access token for authorization."}`), nil
	}
	return jsonResp(`{"code":0,"data":{"bot":{"open_id":"ou_x"}}}`), nil
}

func TestLarkTokenRejectedInvalidatesCacheAndRetries(t *testing.T) {
	cache := newLarkTokenCache()
	fake := &fakeLarkHTTP{rejectFor: "t1"}
	client := lark.NewClient("cli_test", "secret",
		lark.WithTokenCache(cache),
		lark.WithHttpClient(larkTokenHTTPClient{next: fake, cache: cache}),
		lark.WithLogLevel(larkcore.LogLevelError))
	t.Cleanup(func() { lark.NewClient("cli_test", "secret", larkClientOptions()...) })

	resp, err := client.Get(context.Background(), "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(resp.RawBody), `"code":0`) {
		t.Fatalf("response = %s, want success after retry", resp.RawBody)
	}
	if got := strings.Join(fake.apiAuth, ","); got != "Bearer t1,Bearer t2" {
		t.Fatalf("api auth headers = %q, want stale then fresh token", got)
	}
	// The fresh token stays cached for later calls.
	if _, err := client.Get(context.Background(), "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant); err != nil {
		t.Fatal(err)
	}
	if fake.tokens != 2 {
		t.Fatalf("token fetches = %d, want 2", fake.tokens)
	}
}

func TestLarkTokenCacheUsesWallClock(t *testing.T) {
	c := newLarkTokenCache()
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	now := base
	c.now = func() time.Time { return now }
	ctx := context.Background()
	_ = c.Set(ctx, "k", "v", 2*time.Hour)
	now = base.Add(time.Hour)
	if v, _ := c.Get(ctx, "k"); v != "v" {
		t.Fatalf("Get before expiry = %q", v)
	}
	now = base.Add(3 * time.Hour) // wall clock moved past expiry (e.g. suspend)
	if v, _ := c.Get(ctx, "k"); v != "" {
		t.Fatalf("Get after wall-clock expiry = %q, want miss", v)
	}
	_ = c.Set(ctx, "k", "v", 2*time.Hour)
	c.Invalidate("test")
	if v, _ := c.Get(ctx, "k"); v != "" {
		t.Fatalf("Get after Invalidate = %q, want miss", v)
	}
}

func TestLarkWSSupervisorInvalidatesTokensOnSuspend(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	h.sendTick()
	select {
	case <-h.suspends:
		t.Fatal("onSuspend before any suspend")
	default:
	}
	h.clock.Jump(15*time.Second, 40*time.Minute)
	h.sendTick()
	select {
	case <-h.suspends:
	default:
		t.Fatal("onSuspend not called after host suspend")
	}
	h.nextClient()
}

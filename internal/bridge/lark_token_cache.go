package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/rs/zerolog/log"
)

// Feishu error codes meaning the access token we sent is missing, invalid,
// or expired.
var larkInvalidTokenCodes = map[int]bool{
	99991661: true, // missing access token
	99991663: true, // invalid tenant access token
	99991664: true, // invalid app access token
	99991668: true, // invalid/expired token
	99991671: true, // invalid access token
}

// larkTokenCache replaces the SDK's local token cache. The SDK cache stores
// time.Now().Add(ttl) and compares with time.Now(), i.e. monotonic time,
// which does not advance while a VM is paused: after a host suspend a tenant
// token that already expired server-side is still served from cache and every
// API call fails with 99991663 until the process restarts. This cache compares
// wall-clock time and can be invalidated.
type larkTokenCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]larkTokenEntry
}

type larkTokenEntry struct {
	value   string
	expires time.Time
}

func newLarkTokenCache() *larkTokenCache {
	return &larkTokenCache{now: time.Now, m: map[string]larkTokenEntry{}}
}

func (c *larkTokenCache) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return "", nil
	}
	if !c.now().Round(0).Before(e.expires) {
		delete(c.m, key)
		return "", nil
	}
	return e.value, nil
}

func (c *larkTokenCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = larkTokenEntry{value: value, expires: c.now().Round(0).Add(ttl)}
	return nil
}

// Invalidate drops every cached token so the next request fetches a new one.
func (c *larkTokenCache) Invalidate(reason string) {
	c.mu.Lock()
	n := len(c.m)
	c.m = map[string]larkTokenEntry{}
	c.mu.Unlock()
	if n > 0 {
		log.Info().Str("reason", reason).Msg("bridge: lark access token cache invalidated")
	}
}

// larkTokenHTTPClient invalidates the token cache when Feishu rejects the
// token. The SDK's doRequest then retries once with a freshly fetched token
// (it already loops on 99991663/99991664/99991671 but would otherwise resend
// the same stale cached token).
type larkTokenHTTPClient struct {
	next  larkcore.HttpClient
	cache *larkTokenCache
}

func (h larkTokenHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := h.next.Do(req)
	if err != nil || resp == nil || resp.Body == nil ||
		!strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return resp, err
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if rerr != nil {
		return resp, nil
	}
	var out struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(body, &out) == nil && larkInvalidTokenCodes[out.Code] {
		log.Warn().Int("code", out.Code).Str("path", req.URL.Path).
			Msg("bridge: lark rejected access token; refreshing")
		h.cache.Invalidate("token rejected")
	}
	return resp, nil
}

// The SDK keeps one process-wide token manager (larkcore.NewCache replaces a
// package global), so all bridge clients share one cache.
var sharedLarkTokenCache = newLarkTokenCache()

// larkClientOptions are the token-related options every bridge lark client
// must use.
func larkClientOptions(extra ...lark.ClientOptionFunc) []lark.ClientOptionFunc {
	return append([]lark.ClientOptionFunc{
		lark.WithTokenCache(sharedLarkTokenCache),
		lark.WithHttpClient(larkTokenHTTPClient{next: http.DefaultClient, cache: sharedLarkTokenCache}),
	}, extra...)
}

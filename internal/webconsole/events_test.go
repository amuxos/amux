package webconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

// fakeAttach plays one scripted Attach stream: events, then the final error.
type fakeAttach struct {
	grpc.ClientStream
	events []*pb.AttachEvent
	err    error
	index  int
	block  chan struct{} // closed by the test to let a drained stream end
}

func (f *fakeAttach) Recv() (*pb.AttachEvent, error) {
	if f.index < len(f.events) {
		event := f.events[f.index]
		f.index++
		return event, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	<-f.block
	return nil, context.Canceled
}

// streamBackend serves scripted streams in order and records each cursor asked for.
type streamBackend struct {
	testBackend
	mu      sync.Mutex
	streams []*fakeAttach
	failAt  map[int]error // Attach call number (0-based) that fails outright
	afters  []string
}

func (b *streamBackend) Attach(_ context.Context, _, _, after string) (pb.Amuxlet_AttachClient, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	call := len(b.afters)
	b.afters = append(b.afters, after)
	if err := b.failAt[call]; err != nil {
		return nil, err
	}
	if len(b.streams) == 0 {
		return nil, errors.New("no scripted stream")
	}
	next := b.streams[0]
	b.streams = b.streams[1:]
	return next, nil
}
func (b *streamBackend) cursors() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.afters...)
}

// syncWriter is a goroutine-safe ResponseWriter that supports streaming.
type syncWriter struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
}

func (w *syncWriter) Header() http.Header { return w.header }
func (w *syncWriter) WriteHeader(int)     {}
func (w *syncWriter) Flush()              {}
func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func attachEvent(cursor, text string) *pb.AttachEvent {
	return &pb.AttachEvent{Cursor: cursor, Text: text}
}

// followEvents opens the SSE endpoint like a browser would (optionally resuming
// with Last-Event-ID), waits for the wanted text, then closes the connection.
func followEvents(t *testing.T, backend Backend, lastEventID, want string) string {
	t.Helper()
	s, err := New(backend, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := pairSession(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	query := url.Values{"name": {"chat"}, "target_id": {"runtime-a"}}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7892/api/console/events?"+query.Encode(), nil).WithContext(ctx)
	r.AddCookie(cookie)
	if lastEventID != "" {
		r.Header.Set("Last-Event-ID", lastEventID)
	}
	w := &syncWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(w, r) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(w.String(), want) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("timed out waiting for %q in %q", want, w.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("event handler did not stop with the request")
	}
	return w.String()
}

func TestEventsResumeAfterTheLastEventTheBrowserSaw(t *testing.T) {
	backend := &streamBackend{streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-8", "eight"), attachEvent("c-9", "nine")}, block: make(chan struct{})}}}
	body := followEvents(t, backend, "c-7", "nine")
	if got := backend.cursors(); len(got) != 1 || got[0] != "c-7" {
		t.Fatalf("attach cursors = %q", got)
	}
	if !strings.Contains(body, "id: c-8\n") || !strings.Contains(body, "id: c-9\n") || strings.Contains(body, "disconnected") {
		t.Fatalf("resumed stream = %q", body)
	}
}

func TestEventsWithoutACursorReplayFromTheStart(t *testing.T) {
	backend := &streamBackend{streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-1", "one")}, block: make(chan struct{})}}}
	followEvents(t, backend, "", "one")
	if got := backend.cursors(); len(got) != 1 || got[0] != "" {
		t.Fatalf("attach cursors = %q", got)
	}
}

func TestEventsFallBackToFullReplayWhenTheHubRejectsTheCursor(t *testing.T) {
	// The Hub only reports a bad cursor on the first read of the stream...
	backend := &streamBackend{streams: []*fakeAttach{
		{err: errors.New("invalid cursor"), block: make(chan struct{})},
		{events: []*pb.AttachEvent{attachEvent("c-1", "restarted")}, block: make(chan struct{})},
	}}
	body := followEvents(t, backend, "stale-epoch:9", "restarted")
	if got := backend.cursors(); len(got) != 2 || got[0] != "stale-epoch:9" || got[1] != "" {
		t.Fatalf("attach cursors = %q", got)
	}
	if strings.Contains(body, "disconnected") {
		t.Fatalf("a rejected cursor was reported as a disconnect: %q", body)
	}
	// ...or while attaching.
	backend = &streamBackend{failAt: map[int]error{0: errors.New("unusable cursor")}, streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-1", "replayed")}, block: make(chan struct{})}}}
	followEvents(t, backend, "stale-epoch:9", "replayed")
	if got := backend.cursors(); len(got) != 2 || got[0] != "stale-epoch:9" || got[1] != "" {
		t.Fatalf("attach cursors = %q", got)
	}
}

func TestEventsDoNotRetryAfterTheStreamStartedDeliveringEvents(t *testing.T) {
	backend := &streamBackend{streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-8", "eight")}, err: errors.New("stream killed")}}}
	body := followEvents(t, backend, "c-7", "disconnected")
	if got := backend.cursors(); len(got) != 1 {
		t.Fatalf("a delivering stream was re-attached: %q", got)
	}
	if !strings.Contains(body, "id: c-8\n") || !strings.Contains(body, "event: disconnected") {
		t.Fatalf("stream = %q", body)
	}
}

func TestEventsDoNotReplayEverythingWhenAResumedStreamSimplyEnds(t *testing.T) {
	// A clean end of stream is not a rejected cursor: the browser reconnects on its own.
	backend := &streamBackend{streams: []*fakeAttach{{err: io.EOF}}}
	body := followEvents(t, backend, "c-7", "disconnected")
	if got := backend.cursors(); len(got) != 1 || got[0] != "c-7" {
		t.Fatalf("attach cursors = %q", got)
	}
	if !strings.Contains(body, "event: disconnected") {
		t.Fatalf("stream = %q", body)
	}
}

func TestEventsIgnoreUnreasonableResumeCursors(t *testing.T) {
	for name, value := range map[string]string{"oversized": strings.Repeat("x", 513)} {
		backend := &streamBackend{streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-1", "one")}, block: make(chan struct{})}}}
		followEvents(t, backend, value, "one")
		if got := backend.cursors(); len(got) != 1 || got[0] != "" {
			t.Fatalf("%s cursor reached the Hub: %q", name, got)
		}
	}
	// Control characters cannot travel in a header value, but the guard must hold on its own.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header["Last-Event-ID"] = []string{"c-1\nX-Injected: 1"}
	if got := resumeCursor(r); got != "" {
		t.Fatalf("cursor with a line break = %q", got)
	}
}

func TestEventPayloadsStayJSONWithCursorIDs(t *testing.T) {
	backend := &streamBackend{streams: []*fakeAttach{{events: []*pb.AttachEvent{attachEvent("c-1", "hello")}, block: make(chan struct{})}}}
	body := followEvents(t, backend, "", "hello")
	var payload struct {
		Cursor string `json:"cursor"`
		Text   string `json:"text"`
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if payload.Cursor != "c-1" || payload.Text != "hello" {
		t.Fatalf("payload = %+v", payload)
	}
}

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// stream is an open event stream a test reads one event at a time.
type stream struct {
	server *httptest.Server
	body   *bufio.Reader
	cancel context.CancelFunc
}

// openStream connects to a loom's event stream and returns it ready to read.
func openStream(t *testing.T, fix *fixture) *stream {
	t.Helper()
	server := httptest.NewServer(fix.handler)
	ctx, cancel := context.WithCancel(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/l/"+fix.entry.Slug+"/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET events: %s", response.Status)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		cancel()
		t.Fatalf("Content-Type %q, want text/event-stream", got)
	}

	s := &stream{server: server, body: bufio.NewReader(response.Body), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		response.Body.Close()
		server.Close()
	})
	return s
}

// next reads events until one carries a payload, so the retry preamble and the
// heartbeat comments do not have to be counted by every test.
func (s *stream) next(t *testing.T) SnapshotEvent {
	t.Helper()
	type result struct {
		event SnapshotEvent
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var name, data string
		for {
			line, err := s.body.ReadString('\n')
			if err != nil {
				done <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if name == "" || data == "" {
					name, data = "", ""
					continue
				}
				var event SnapshotEvent
				err := json.Unmarshal([]byte(data), &event)
				done <- result{event: event, err: err}
				return
			}
		}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("reading the event stream: %v", got.err)
		}
		return got.event
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
		return SnapshotEvent{}
	}
}

// A client that connects after a change would otherwise sit on whatever it
// loaded with, so the stream opens by saying where things stand.
func TestEventsOpenWithTheCurrentState(t *testing.T) {
	fix := newFixture(t, nil)
	fix.cache.Seed(projectionFor(fix.root))

	got := openStream(t, fix).next(t)
	if got.Generation != 1 {
		t.Fatalf("first event generation %d, want 1", got.Generation)
	}
	if got.Refreshing || got.Stale || got.Error != "" {
		t.Fatalf("first event %+v, want a settled state", got)
	}
	if got.CapturedAt == nil {
		t.Fatal("first event has no captured_at")
	}
}

// The run and the result are both changes to the published state, so both are
// pushed: a multi-second snapshot must not look like a frozen page.
func TestEventsPushRefreshingThenTheGeneration(t *testing.T) {
	release := make(chan struct{})
	var fix *fixture
	fix = newFixture(t, func(ctx context.Context) (*snapshot.Snapshot, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return projectionFor(fix.root), nil
	})
	fix.cache.Seed(projectionFor(fix.root))

	events := openStream(t, fix)
	if got := events.next(t); got.Refreshing {
		t.Fatalf("the stream opened mid-refresh: %+v", got)
	}

	fix.cache.Refresh()
	running := events.next(t)
	if !running.Refreshing {
		t.Fatalf("a run started and the stream said %+v", running)
	}
	if running.Generation != 1 {
		t.Fatalf("a run in flight moved the generation to %d", running.Generation)
	}

	close(release)
	landed := events.next(t)
	if landed.Refreshing {
		t.Fatalf("the run finished and the stream still says refreshing: %+v", landed)
	}
	if landed.Generation != 2 {
		t.Fatalf("generation %d after one refresh over a seed, want 2", landed.Generation)
	}
}

// A refresh that fails is still news: the projection did not move, but what
// the reader should think of it did.
func TestEventsPushAFailedRefresh(t *testing.T) {
	fix := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) {
		return nil, errors.New("loom.sh: exit status 2")
	})
	fix.cache.Seed(projectionFor(fix.root))

	events := openStream(t, fix)
	events.next(t)

	fix.cache.Refresh()
	var got SnapshotEvent
	for attempt := 0; attempt < 4; attempt++ {
		got = events.next(t)
		if !got.Refreshing {
			break
		}
	}
	if !got.Stale || got.Error == "" {
		t.Fatalf("a failed refresh pushed %+v, want stale with a reason", got)
	}
	if got.Generation != 1 {
		t.Fatalf("a failed refresh moved the generation to %d", got.Generation)
	}
}

// The event says what changed and never what it changed to. A projection is
// 162 KB on a large loom; pushing it down every stream would make looking at a
// loom cost more the more people did.
func TestEventsCarryNoProjection(t *testing.T) {
	fix := newFixture(t, nil)
	fix.cache.Seed(projectionFor(fix.root))

	server := httptest.NewServer(fix.handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/l/"+fix.entry.Slug+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	buf := make([]byte, 512)
	n, _ := response.Body.Read(buf)
	if body := string(buf[:n]); strings.Contains(body, "stitches") || strings.Contains(body, "snapshot_api") {
		t.Fatalf("the stream carried projection data: %q", body)
	}
}

func TestEventsUnknownLoomIsJSON(t *testing.T) {
	fix := newFixture(t, nil)
	rec := httptest.NewRecorder()
	fix.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/l/nope/events", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type %q, want JSON", got)
	}
}

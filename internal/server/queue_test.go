package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	loomcmd "github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

// queueFixture is a registry with one loom, a controllable projection, and a
// stub loom.sh that records the argv it was invoked with.
//
// The argv is the assertion that matters most here. This package must never
// build a shell string or edit .loom/queue, so what the endpoints are really
// tested on is that each one turns into exactly the loom command the protocol
// names, with the moved ID first.
type queueFixture struct {
	handler http.Handler
	entry   *Entry
	cache   *snapshot.Cache
	runs    *atomic.Int64
	argv    func() []string
}

func newQueueFixture(t *testing.T, snap *snapshot.Snapshot, script string) *queueFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub loom script needs a POSIX shell")
	}

	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	root := filepath.Join(dir, ".loom")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "loom.sh")
	record := filepath.Join(dir, "argv")
	preamble := "#!/usr/bin/env bash\nprintf '%s ' \"$@\" >> " + record + "\nprintf '\\n' >> " + record + "\n"
	if err := os.WriteFile(path, []byte(preamble+script), 0o755); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int64
	cache := snapshot.NewCache(func(context.Context) (*snapshot.Snapshot, error) {
		runs.Add(1)
		return snap, nil
	})
	t.Cleanup(cache.Close)
	cache.Seed(snap)

	registry := NewRegistry()
	entry := registry.Add(Loom{
		Target:  target.Target{Script: path, Root: root},
		Version: target.Version{Tier: target.TierV2, FormatVersion: 2, SchemaVersion: 1},
		Cache:   cache,
		Mutator: loomcmd.New(path, root, func() *snapshot.Snapshot {
			return cache.Current().Snapshot
		}),
	})

	return &queueFixture{
		handler: NewRouter(RouterOptions{Registry: registry}),
		entry:   entry,
		cache:   cache,
		runs:    &runs,
		argv: func() []string {
			data, err := os.ReadFile(record)
			if err != nil {
				return nil
			}
			return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		},
	}
}

func (f *queueFixture) send(t *testing.T, command string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/l/"+f.entry.Slug+"/queue/"+command, strings.NewReader(string(encoded)))
	f.handler.ServeHTTP(rec, req)
	return rec
}

// queueLoom has one queued stitch, one unqueued stitch and one tied one, which
// is enough for every anchor rule.
func queueLoom(root string) *snapshot.Snapshot {
	position := 1
	return &snapshot.Snapshot{
		SchemaVersion: 1,
		FormatVersion: 2,
		LoomRoot:      root,
		Frontier:      []string{"http-server"},
		Stitches: []snapshot.Stitch{
			{
				ID: "http-server", RootID: "web-shell", Path: "threads/web-shell/http-server",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
				QueuePosition: &position,
			},
			{
				ID: "embedded-assets", RootID: "web-shell", Path: "threads/web-shell/embedded-assets",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain,
			},
			{
				ID: "shell-skeleton", RootID: "web-shell", Path: "threads/web-shell/shell-skeleton.tied",
				Tray: snapshot.TrayThreads, State: snapshot.StateTied,
			},
		},
	}
}

// accepts is a stub loom.sh that emits a v1 mutation result for whatever it was
// asked to do.
const accepts = `
command="$1"; shift
[ "$1" = "--json" ] && shift
cat <<JSON
{"schema_version":1,"format_version":2,"command":"$command","ok":true,"changed":true,
 "id":"$1","state":"plain","path":"threads/web-shell/$1","tray":"threads",
 "queue_position":1,"completed_at":null}
JSON
`

// Each endpoint is exactly one loom command, and the moved ID comes first.
// Getting before/after backwards silently reorders the wrong stitch, which is
// the whole reason neither frontend is allowed to build this argv.
func TestEachEndpointIsOneLoomCommand(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		body     QueueRequest
		want     string
	}{
		{"append", QueueRequest{ID: "embedded-assets"}, "queue --json embedded-assets"},
		{"first", QueueRequest{ID: "embedded-assets"}, "first --json embedded-assets"},
		{"before", QueueRequest{ID: "embedded-assets", Anchor: "http-server"},
			"before --json embedded-assets http-server"},
		{"after", QueueRequest{ID: "embedded-assets", Anchor: "http-server"},
			"after --json embedded-assets http-server"},
		{"unqueue", QueueRequest{ID: "http-server"}, "unqueue --json http-server"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
			rec := f.send(t, tc.endpoint, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			argv := f.argv()
			if len(argv) != 1 || strings.TrimSpace(argv[0]) != tc.want {
				t.Errorf("ran %q, want %q", argv, tc.want)
			}
		})
	}
}

// The queue file is protocol state and this package never touches it. What the
// response carries is the loom's own result, not a re-derived projection.
func TestTheResponseIsTheLoomsOwnResult(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "first", QueueRequest{ID: "embedded-assets"})

	body := decode[QueueResponse](t, rec)
	if body.Command != "first" || body.ID != "embedded-assets" {
		t.Errorf("result %+v", body)
	}
	if !body.Structured || !body.Changed {
		t.Errorf("structured=%v changed=%v, want both true", body.Structured, body.Changed)
	}
	if body.QueuePosition == nil || *body.QueuePosition != 1 {
		t.Errorf("queue position %v, want 1", body.QueuePosition)
	}
	if body.Tray != snapshot.TrayThreads || body.State != snapshot.StatePlain {
		t.Errorf("state %q tray %q", body.State, body.Tray)
	}
}

// A queue write renumbers every position after the moved one, and a tie or drop
// elsewhere evicts IDs without anyone touching the queue. So the projection is
// always re-taken; the browser learns the new one over the events stream.
func TestAMutationAsksForANewProjection(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	before := f.runs.Load()

	if rec := f.send(t, "append", QueueRequest{ID: "embedded-assets"}); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.runs.Load() <= before {
		if time.Now().After(deadline) {
			t.Fatalf("map ran %d times, was %d — nothing re-snapshotted", f.runs.Load(), before)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An unrelated stale queue record makes an unrelated mutation fail *without
// changing the file*. Only the loom's own words explain that, so they travel
// verbatim rather than paraphrased.
func TestARefusalCarriesTheLoomsOwnWords(t *testing.T) {
	const refuses = `
echo 'queue records are invalid: unknown queue entry '"'"'ghost-stitch'"'"' at position 6' >&2
cat <<'JSON'
{"schema_version":1,"format_version":2,"command":"queue","ok":false,"changed":false,
 "error":{"code":"queue_records",
          "message":"queue records are invalid: unknown queue entry 'ghost-stitch' at position 6",
          "stitch_ids":["ghost-stitch"]}}
JSON
exit 1
`
	f := newQueueFixture(t, queueLoom(t.TempDir()), refuses)
	rec := f.send(t, "append", QueueRequest{ID: "embedded-assets"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	body := decode[apiError](t, rec)
	if body.Error.Code != "loom-refused" {
		t.Errorf("code %q", body.Error.Code)
	}
	if body.Error.LoomCode != string(loomcmd.CodeQueueRecords) {
		t.Errorf("loom code %q, want queue_records", body.Error.LoomCode)
	}
	if !strings.Contains(body.Error.Message, "ghost-stitch") {
		t.Errorf("message %q does not carry the loom's words", body.Error.Message)
	}
	if !strings.Contains(body.Error.Stderr, "ghost-stitch") {
		t.Errorf("stderr %q is not verbatim", body.Error.Stderr)
	}
	if len(body.Error.StitchIDs) != 1 || body.Error.StitchIDs[0] != "ghost-stitch" {
		t.Errorf("stitch ids %v", body.Error.StitchIDs)
	}
}

// The queue lock being held by another writer is a normal outcome and the one
// case where retrying the identical request is right, so it is not a 409.
func TestAHeldQueueLockIsRetryable(t *testing.T) {
	const locked = `
cat <<'JSON'
{"schema_version":1,"format_version":2,"command":"queue","ok":false,"changed":false,
 "error":{"code":"queue_locked","message":"another writer holds the queue lock"}}
JSON
exit 1
`
	f := newQueueFixture(t, queueLoom(t.TempDir()), locked)
	rec := f.send(t, "append", QueueRequest{ID: "embedded-assets"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503: %s", rec.Code, rec.Body)
	}
}

// The protocol's two anchor rules, refused here rather than surfaced as a raw
// loom error — and refused before anything runs.
func TestAnImpossibleAnchorIsRefusedBeforeAnythingRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   QueueRequest
		status int
		code   string
	}{
		{"the anchor is the moved stitch", QueueRequest{ID: "http-server", Anchor: "http-server"},
			http.StatusConflict, "queue-anchor"},
		{"the anchor is not on the queue", QueueRequest{ID: "http-server", Anchor: "embedded-assets"},
			http.StatusConflict, "queue-anchor"},
		{"the anchor is not a stitch", QueueRequest{ID: "http-server", Anchor: "no-such-thing"},
			http.StatusNotFound, "unknown-stitch"},
		{"there is no anchor at all", QueueRequest{ID: "http-server"},
			http.StatusBadRequest, "bad-request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
			rec := f.send(t, "before", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if got := decode[apiError](t, rec).Error.Code; got != tc.code {
				t.Errorf("code %q, want %q", got, tc.code)
			}
			if argv := f.argv(); len(argv) != 0 {
				t.Errorf("the loom ran anyway: %q", argv)
			}
		})
	}
}

// An ID the projection does not have never reaches the loom.
func TestAnUnknownStitchNeverReachesTheLoom(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "append", QueueRequest{ID: "no-such-stitch"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	if argv := f.argv(); len(argv) != 0 {
		t.Errorf("the loom ran anyway: %q", argv)
	}
}

// unqueue is the repair path: the protocol accepts any syntactically valid ID
// for it so a record naming a stitch that is not there can be removed. Refusing
// it here would make the one command that repairs a stale queue unreachable.
func TestUnqueueReachesARecordWithNoStitchBehindIt(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "unqueue", QueueRequest{ID: "ghost-stitch"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if argv := f.argv(); len(argv) != 1 || !strings.Contains(argv[0], "unqueue --json ghost-stitch") {
		t.Errorf("ran %q", argv)
	}
}

// Nothing here interpolates a string into a command. An ID that is not a stitch
// ID is refused by shape, before the projection is even consulted.
func TestAnIDThatIsNotAStitchIDIsRefusedByShape(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "unqueue", QueueRequest{ID: "; rm -rf /"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := decode[apiError](t, rec).Error.Code; got != "invalid-stitch" {
		t.Errorf("code %q, want invalid-stitch", got)
	}
	if argv := f.argv(); len(argv) != 0 {
		t.Errorf("the loom ran anyway: %q", argv)
	}
}

// A loom opened read-only says so rather than pretending, the way the terminal's
// lifecycle keys do.
func TestAReadOnlyLoomRefusesToMutate(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	f.entry.Mutator = nil

	rec := f.send(t, "append", QueueRequest{ID: "embedded-assets"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := decode[apiError](t, rec).Error.Code; got != "read-only" {
		t.Errorf("code %q, want read-only", got)
	}
}

// A body that is not a queue command says so, and an unknown field is a typo
// worth naming rather than silently ignoring.
func TestABadBodyIsRefused(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)

	for _, body := range []string{`not json`, `{"id":"http-server","postion":2}`, `{}`} {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
			"/api/l/"+f.entry.Slug+"/queue/append", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
	}
}

// An anchor on a command that has no anchor is a caller confusing two
// endpoints, and silently dropping it would reorder against the wrong thing.
func TestAnAnchorOnAnAnchorlessCommandIsRefused(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "append", QueueRequest{ID: "embedded-assets", Anchor: "http-server"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400: %s", rec.Code, rec.Body)
	}
}

// The endpoints are on the API tree, so an unknown one answers as an endpoint
// rather than being handed the HTML shell.
func TestAnUnknownQueueEndpointIsNotTheFrontend(t *testing.T) {
	f := newQueueFixture(t, queueLoom(t.TempDir()), accepts)
	rec := f.send(t, "shuffle", QueueRequest{ID: "http-server"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404: %s", rec.Code, rec.Body)
	}
}

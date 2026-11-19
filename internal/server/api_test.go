package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

// fixture is a registry with one loom on disk, its cache, and the number of
// times map has been run for it.
type fixture struct {
	handler http.Handler
	entry   *Entry
	cache   *snapshot.Cache
	runs    *atomic.Int64
	root    string
}

// newFixture deploys a loom directory holding one stitch with instructions,
// and registers it with a cache whose source the test controls.
func newFixture(t *testing.T, source snapshot.Source) *fixture {
	t.Helper()
	parent := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	root := filepath.Join(parent, ".loom")
	stitchDir := filepath.Join(root, "threads", "web-shell", "snapshot-api")
	if err := os.MkdirAll(stitchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stitchDir, "instructions.md"), []byte("# Snapshot API\n\nServe the cache.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int64
	counted := func(ctx context.Context) (*snapshot.Snapshot, error) {
		runs.Add(1)
		return source(ctx)
	}
	cache := snapshot.NewCache(counted)
	t.Cleanup(cache.Close)

	registry := NewRegistry()
	entry := registry.Add(Loom{
		Target: target.Target{Script: filepath.Join(root, "loom.sh"), Root: root},
		Version: target.Version{
			Tier:          target.TierV2,
			FormatVersion: 2,
			SchemaVersion: 1,
		},
		Cache: cache,
	})
	return &fixture{handler: NewRouter(RouterOptions{Registry: registry}), entry: entry, cache: cache, runs: &runs, root: root}
}

// projectionFor is a projection with one stitch, one diagnostic and one
// dependency edge, so the API can be checked to pass them all through.
func projectionFor(root string) *snapshot.Snapshot {
	position := 3
	target := "http-server"
	return &snapshot.Snapshot{
		SchemaVersion: 1,
		FormatVersion: 2,
		LoomRoot:      root,
		Stitches: []snapshot.Stitch{{
			ID:            "snapshot-api",
			RootID:        "web-shell",
			Path:          "threads/web-shell/snapshot-api",
			Tray:          snapshot.TrayThreads,
			State:         snapshot.StatePlain,
			Ready:         true,
			QueuePosition: &position,
			Children:      []string{},
			Dependencies: []snapshot.Dependency{{
				From: "snapshot-api", To: "http-server", Status: snapshot.DependencySatisfied,
			}},
			Cycle: []string{},
		}},
		Frontier: []string{"snapshot-api"},
		Diagnostics: []snapshot.Diagnostic{{
			Severity: snapshot.SeverityError,
			Code:     "broken-dependency",
			Message:  "graph-layout needs a stitch that is not there",
			TargetID: &target,
		}},
	}
}

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (f *fixture) post(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q, want JSON", got)
	}
	return body
}

func TestAPIListsTheLooms(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })

	rec := f.get(t, "/api/looms")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	list := decode[LoomList](t, rec)
	if len(list.Looms) != 1 {
		t.Fatalf("listed %d looms, want 1", len(list.Looms))
	}
	loom := list.Looms[0]
	if loom.Slug != f.entry.Slug || loom.Name != f.entry.Display {
		t.Errorf("listed %q/%q, want %q/%q", loom.Slug, loom.Name, f.entry.Slug, f.entry.Display)
	}
	if loom.Root != f.root || loom.Tier != "v2" || loom.FormatVersion != 2 || loom.SchemaVersion != 1 {
		t.Errorf("listing does not describe the loom: %+v", loom)
	}
	if list.Primary != f.entry.Slug {
		t.Errorf("primary %q, want %q", list.Primary, f.entry.Slug)
	}
}

// The rule the whole API is shaped around: a read serves the cache and never
// waits on map.
func TestSnapshotServesTheCacheWithoutRunningMap(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) {
		t.Error("a snapshot read ran map")
		return nil, errors.New("map must not run here")
	})
	f.cache.Seed(projectionFor(f.root))

	rec := f.get(t, "/api/l/"+f.entry.Slug+"/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := decode[SnapshotResponse](t, rec)
	if body.Snapshot == nil || len(body.Snapshot.Stitches) != 1 {
		t.Fatalf("projection not served: %+v", body.Snapshot)
	}
	if body.Generation != 1 || body.Stale || body.Error != "" {
		t.Errorf("metadata %+v, want a fresh generation 1", body)
	}
	if body.CapturedAt == nil {
		t.Error("captured_at is null for a projection that exists")
	}
	if runs := f.runs.Load(); runs != 0 {
		t.Errorf("map ran %d times serving a cached read, want 0", runs)
	}
}

// Diagnostics ride along untouched: a problems pane renders them verbatim.
func TestSnapshotPassesDiagnosticsAndEdgesThrough(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	f.cache.Seed(projectionFor(f.root))

	body := decode[SnapshotResponse](t, f.get(t, "/api/l/"+f.entry.Slug+"/snapshot"))
	if len(body.Snapshot.Diagnostics) != 1 {
		t.Fatalf("%d diagnostics, want 1", len(body.Snapshot.Diagnostics))
	}
	diagnostic := body.Snapshot.Diagnostics[0]
	if diagnostic.Severity != snapshot.SeverityError || diagnostic.Code != "broken-dependency" {
		t.Errorf("diagnostic altered in transit: %+v", diagnostic)
	}
	if diagnostic.TargetID == nil || *diagnostic.TargetID != "http-server" {
		t.Errorf("diagnostic target lost: %+v", diagnostic)
	}
	stitch := body.Snapshot.Stitches[0]
	if stitch.QueuePosition == nil || *stitch.QueuePosition != 3 {
		t.Errorf("queue position lost: %+v", stitch.QueuePosition)
	}
	if len(stitch.Dependencies) != 1 || stitch.Dependencies[0].Status != snapshot.DependencySatisfied {
		t.Errorf("dependencies lost: %+v", stitch.Dependencies)
	}
	if !stitch.Ready {
		t.Error("ready is authoritative and was not carried through")
	}
}

// A broken loom shows the last good projection behind an error, never nothing.
func TestSnapshotReportsStalenessAndKeepsTheProjection(t *testing.T) {
	var fail atomic.Bool
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) {
		if fail.Load() {
			return nil, errors.New("loom.sh: exit status 2")
		}
		return projectionFor(""), nil
	})
	if _, err := f.cache.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, err := f.cache.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}

	body := decode[SnapshotResponse](t, f.get(t, "/api/l/"+f.entry.Slug+"/snapshot"))
	if !body.Stale {
		t.Error("a failed refresh was not reported as stale")
	}
	if !strings.Contains(body.Error, "exit status 2") {
		t.Errorf("error %q does not say what failed", body.Error)
	}
	if body.Snapshot == nil {
		t.Fatal("the last good projection was blanked by a failed refresh")
	}
	if body.Generation != 1 {
		t.Errorf("generation %d, want the last published one", body.Generation)
	}
}

// Before the first successful refresh there is nothing to serve, and the
// response says so rather than pretending an empty loom.
func TestSnapshotBeforeAnyProjection(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })

	body := decode[SnapshotResponse](t, f.get(t, "/api/l/"+f.entry.Slug+"/snapshot"))
	if body.Snapshot != nil || body.Generation != 0 || body.CapturedAt != nil {
		t.Errorf("want an empty envelope, got %+v", body)
	}
}

func TestRefreshReturnsAtOnceAndRunsInTheBackground(t *testing.T) {
	release := make(chan struct{})
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) {
		<-release
		return projectionFor(""), nil
	})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.post(t, "/api/l/"+f.entry.Slug+"/refresh") }()

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("refresh waited for map to finish")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}
	if body := decode[RefreshResponse](t, rec); !body.Accepted {
		t.Error("refresh was not acknowledged")
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for f.cache.Current().Generation == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the refresh never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRefreshRejectsAGet(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	if rec := f.get(t, "/api/l/"+f.entry.Slug+"/refresh"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d for GET refresh, want 405", rec.Code)
	}
}

func TestStitchServesTheRecordAndItsInstructions(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	f.cache.Seed(projectionFor(f.root))

	rec := f.get(t, "/api/l/"+f.entry.Slug+"/stitch/snapshot-api")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := decode[StitchResponse](t, rec)
	if body.Stitch.ID != "snapshot-api" || !body.Stitch.Ready {
		t.Errorf("stitch record wrong: %+v", body.Stitch)
	}
	if !strings.Contains(body.Instructions, "Serve the cache.") {
		t.Errorf("instructions %q do not carry the file body", body.Instructions)
	}
	if body.InstructionsPath != "threads/web-shell/snapshot-api/instructions.md" {
		t.Errorf("instructions path %q", body.InstructionsPath)
	}
	if body.InstructionsError != "" {
		t.Errorf("unexpected instructions error: %s", body.InstructionsError)
	}
	if runs := f.runs.Load(); runs != 0 {
		t.Errorf("map ran %d times serving a stitch, want 0", runs)
	}
}

// A stitch whose instructions cannot be read is still worth rendering.
func TestStitchWithoutReadableInstructions(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	projection := projectionFor(f.root)
	projection.Stitches[0].Path = "threads/web-shell/vanished"
	f.cache.Seed(projection)

	rec := f.get(t, "/api/l/"+f.entry.Slug+"/stitch/snapshot-api")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: a missing instructions file is not a failed request", rec.Code)
	}
	body := decode[StitchResponse](t, rec)
	if body.Instructions != "" {
		t.Errorf("instructions %q, want empty", body.Instructions)
	}
	if body.InstructionsError == "" {
		t.Error("nothing explained the missing instructions")
	}
}

func TestStitchUnknownID(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	f.cache.Seed(projectionFor(f.root))

	rec := f.get(t, "/api/l/"+f.entry.Slug+"/stitch/not-a-stitch")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if code := decode[apiError](t, rec).Error.Code; code != "unknown-stitch" {
		t.Errorf("error code %q, want unknown-stitch", code)
	}
}

func TestStitchBeforeAnyProjection(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })

	rec := f.get(t, "/api/l/"+f.entry.Slug+"/stitch/snapshot-api")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if code := decode[apiError](t, rec).Error.Code; code != "no-snapshot" {
		t.Errorf("error code %q, want no-snapshot", code)
	}
}

// An API 404 is JSON, not the HTML router's plain text: a fetch should not
// have to guess what it got.
func TestAPIUnknownLoomIsJSON(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })

	for _, path := range []string{"/api/l/gone/snapshot", "/api/l/gone/stitch/x"} {
		rec := f.get(t, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404", path, rec.Code)
		}
		body := decode[apiError](t, rec)
		if body.Error.Code != "unknown-loom" || !strings.Contains(body.Error.Message, f.entry.Slug) {
			t.Errorf("GET %s: error %+v does not say which looms exist", path, body.Error)
		}
	}
}

// The API is served beside the views, not instead of them.
func TestAPIDoesNotShadowTheViews(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	if rec := f.get(t, "/l/"+f.entry.Slug+"/frontier"); rec.Code != http.StatusOK {
		t.Errorf("status %d for the frontier view, want 200", rec.Code)
	}
}

// Reading instructions must not be a way to read the rest of the disk.
func TestReadInstructionsStaysInsideTheLoom(t *testing.T) {
	root := t.TempDir()
	if _, _, err := readInstructions(root, "threads/../../elsewhere"); err == nil {
		t.Fatal("a path escaping the loom directory was read")
	}
}

// The stitch directory is where the protocol puts notes, logs and decisions,
// and reason.md on a dropped stitch. They travel with the record, so they are
// part of what a stitch entails.
func TestStitchListsItsArtifacts(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	f.cache.Seed(projectionFor(f.root))

	dir := filepath.Join(f.root, "threads", "web-shell", "snapshot-api")
	write := func(path string, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", "what happened")
	write("reason.md", "dropped because")
	write("logs/run.txt", "output")                    // an opaque supporting directory
	write("needs/http-server", "")                     // protocol state, not an artifact
	write("child-stitch/instructions.md", "# a child") // decomposition, not an artifact

	body := decode[StitchResponse](t, f.get(t, "/api/l/"+f.entry.Slug+"/stitch/snapshot-api"))
	got := map[string]Artifact{}
	for _, artifact := range body.Artifacts {
		got[artifact.Name] = artifact
	}

	for _, name := range []string{"notes.md", "reason.md", "logs"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is not listed as an artifact: %+v", name, body.Artifacts)
		}
	}
	for _, name := range []string{instructionsName, needsDirName, completedAtName, "child-stitch"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s was listed as an artifact; it is not one", name)
		}
	}
	if !got["logs"].Dir {
		t.Error("a directory artifact is not marked as one")
	}
	if got["notes.md"].Size != int64(len("what happened")) {
		t.Errorf("notes.md size %d, want %d", got["notes.md"].Size, len("what happened"))
	}
	if body.ArtifactsError != "" {
		t.Errorf("unexpected artifacts error: %s", body.ArtifactsError)
	}
}

// A stitch directory that cannot be read is still worth rendering.
func TestStitchWithoutAReadableDirectory(t *testing.T) {
	f := newFixture(t, func(context.Context) (*snapshot.Snapshot, error) { return nil, nil })
	projection := projectionFor(f.root)
	projection.Stitches[0].Path = "threads/web-shell/vanished"
	f.cache.Seed(projection)

	body := decode[StitchResponse](t, f.get(t, "/api/l/"+f.entry.Slug+"/stitch/snapshot-api"))
	if len(body.Artifacts) != 0 {
		t.Errorf("artifacts %+v, want none", body.Artifacts)
	}
	if body.ArtifactsError == "" {
		t.Error("nothing explained the unreadable stitch directory")
	}
}

func TestReadArtifactsStaysInsideTheLoom(t *testing.T) {
	root := t.TempDir()
	if _, err := readArtifacts(root, "threads/../../elsewhere"); err == nil {
		t.Fatal("a path escaping the loom directory was listed")
	}
}

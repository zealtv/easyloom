package target

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// projection is a minimal map document. Its loom_root is filled in per test,
// because verifying the root is one of the things being tested.
const projection = `{
  "schema_version": %SCHEMA%,
  "format_version": 2,
  "loom_root": "%ROOT%",
  "stitches": [],
  "decomposition_edges": [],
  "dependency_edges": [],
  "cycles": [],
  "frontier": [],
  "recently_completed": [],
  "diagnostics": []
}`

// probeLoom deploys a stub loom whose map behaviour the test dictates: body is
// the shell run for `map --json`, and marker is the format-version file
// contents ("" writes no marker, which is what makes a loom v1).
func probeLoom(t *testing.T, marker, body string) Target {
	t.Helper()
	root := t.TempDir()
	loomDir := filepath.Join(root, loomDirName)
	if err := os.MkdirAll(loomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(loomDir, "loom.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(loomDir, markerName), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target, err := Resolve(script)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// emits builds a stub body that prints a projection naming the given root.
func emits(schema, root string) string {
	doc := strings.NewReplacer("%SCHEMA%", schema, "%ROOT%", root).Replace(projection)
	return "cat <<'JSON'\n" + doc + "\nJSON\n"
}

func TestProbeV2(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("1", target.Root)), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if version.Tier != TierV2 {
		t.Fatalf("tier = %q, want %q", version.Tier, TierV2)
	}
	if !version.Full() {
		t.Error("v2 target is not full-featured")
	}
	if version.Snapshot == nil {
		t.Error("probe discarded the snapshot it paid for")
	}
	if version.SchemaVersion != 1 || version.FormatVersion != 2 {
		t.Errorf("schema %d format %d, want 1 and 2", version.SchemaVersion, version.FormatVersion)
	}
}

// Schema 2 only added the inert dependency status, so a loom on either schema
// is fully driveable: a newer loom.sh and one that predates the bump.
func TestProbeV2AcceptsSchema2(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("2", target.Root)), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if version.Tier != TierV2 || version.SchemaVersion != 2 {
		t.Fatalf("tier %q schema %d, want %q and 2", version.Tier, version.SchemaVersion, TierV2)
	}
}

// The real v1 signal, verbatim from a markerless loom carrying a v2 script.
func TestProbeV1Refusal(t *testing.T) {
	target := probeLoom(t, "",
		"echo 'error: map requires a format v2 loom (run migrate-v2 first)' >&2\nexit 1\n")

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe: %v, want a v1 tier rather than an error", err)
	}
	if version.Tier != TierV1 {
		t.Fatalf("tier = %q, want %q", version.Tier, TierV1)
	}
	if version.Full() {
		t.Error("v1 target reported as full-featured; it must be read-only")
	}
	if !strings.Contains(version.Refusal, "migrate-v2") {
		t.Errorf("refusal = %q, want the loom's own message", version.Refusal)
	}
	if version.FormatVersion != 0 {
		t.Errorf("format version = %d, want 0 for a markerless loom", version.FormatVersion)
	}
}

// A script old enough to predate map has no such command.
func TestProbeV1UnknownCommand(t *testing.T) {
	target := probeLoom(t, "2\n", "echo 'unknown command: map' >&2\nexit 64\n")

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if version.Tier != TierV1 {
		t.Errorf("tier = %q, want %q", version.Tier, TierV1)
	}
}

// A marked v2 loom that fails for some unrelated reason is broken, not v1.
// Downgrading it to read-only would hide a real fault behind a wrong story.
func TestProbeBrokenV2IsNotV1(t *testing.T) {
	target := probeLoom(t, "2\n", "echo 'error: permission denied reading threads/' >&2\nexit 1\n")

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err == nil {
		t.Fatalf("got tier %q and no error, want the failure surfaced", version.Tier)
	}
	if version.Tier == TierV1 {
		t.Error("a broken v2 loom was downgraded to v1")
	}
}

// Parser compatibility is gated on schema_version, not format_version.
func TestProbeUnsupportedSchema(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("7", target.Root)), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if version.Tier != TierUnsupported {
		t.Fatalf("tier = %q, want %q", version.Tier, TierUnsupported)
	}
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *UnsupportedError", err)
	}
	if unsupported.SchemaVersion != 7 {
		t.Errorf("error names schema %d, want 7", unsupported.SchemaVersion)
	}
	if !strings.Contains(unsupported.Error(), "7") {
		t.Errorf("message does not name the version seen: %s", unsupported)
	}
}

// Cheap proof we are driving the loom we resolved.
func TestProbeRejectsMismatchedRoot(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("1", "/somewhere/else/.loom")), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	_, err := prober.Probe(context.Background(), target)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *MismatchError", err)
	}
	if mismatch.Reported != "/somewhere/else/.loom" || mismatch.Resolved != target.Root {
		t.Errorf("mismatch = %+v, want both roots named", mismatch)
	}
}

// The probe costs a whole snapshot, so it must happen once per target.
func TestProbeCachesTierPerTarget(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	counter := filepath.Join(t.TempDir(), "runs")
	body := "echo x >> " + counter + "\n" + emits("1", target.Root)
	if err := os.WriteFile(target.Script, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	for i := 0; i < 5; i++ {
		if _, err := prober.Probe(context.Background(), target); err != nil {
			t.Fatal(err)
		}
	}
	if runs := countLines(t, counter); runs != 1 {
		t.Fatalf("map ran %d times, want 1", runs)
	}

	prober.Forget(target)
	if _, err := prober.Probe(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if runs := countLines(t, counter); runs != 2 {
		t.Fatalf("map ran %d times after Forget, want 2", runs)
	}
}

// A cached probe must report the same refusal the first one did, not silently
// succeed on the second call.
func TestProbeCachedUnsupportedStillErrors(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("7", target.Root)), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	if _, err := prober.Probe(context.Background(), target); err == nil {
		t.Fatal("want an error on the first probe")
	}
	var unsupported *UnsupportedError
	if _, err := prober.Probe(context.Background(), target); !errors.As(err, &unsupported) {
		t.Fatalf("cached probe returned %v, want *UnsupportedError", err)
	}
}

// Concurrent probes of one target share a single run rather than each paying
// for a snapshot.
func TestProbeConcurrentSharesOneRun(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	counter := filepath.Join(t.TempDir(), "runs")
	body := "echo x >> " + counter + "\nsleep 0.2\n" + emits("1", target.Root)
	if err := os.WriteFile(target.Script, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version, err := prober.Probe(context.Background(), target)
			if err != nil {
				t.Error(err)
				return
			}
			if version.Tier != TierV2 {
				t.Errorf("tier = %q, want %q", version.Tier, TierV2)
			}
		}()
	}
	wg.Wait()

	if runs := countLines(t, counter); runs != 1 {
		t.Fatalf("map ran %d times across concurrent probes, want 1", runs)
	}
}

// A probe abandoned by its caller says nothing about the loom, so it must not
// be remembered as a verdict.
func TestProbeDoesNotCacheContextFailure(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	if err := os.WriteFile(target.Script,
		[]byte("#!/usr/bin/env bash\n"+emits("1", target.Root)), 0o755); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	var prober Prober
	if _, err := prober.Probe(cancelled, target); err == nil {
		t.Fatal("want an error from a cancelled probe")
	}

	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe after cancellation: %v", err)
	}
	if version.Tier != TierV2 {
		t.Errorf("tier = %q, want %q", version.Tier, TierV2)
	}
}

// Schema drift is reported without changing the tier: the projection is still
// usable.
func TestProbeDriftKeepsTier(t *testing.T) {
	target := probeLoom(t, "2\n", "")
	drifted := strings.Replace(emits("1", target.Root), `"diagnostics": []`, `"diagnostics": [], "weft": 3`, 1)
	if err := os.WriteFile(target.Script, []byte("#!/usr/bin/env bash\n"+drifted), 0o755); err != nil {
		t.Fatal(err)
	}

	var prober Prober
	version, err := prober.Probe(context.Background(), target)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if version.Tier != TierV2 {
		t.Errorf("tier = %q, want %q", version.Tier, TierV2)
	}
	var drift *snapshot.DriftError
	if !errors.As(version.Drift, &drift) {
		t.Fatalf("Drift = %v, want a *snapshot.DriftError", version.Drift)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

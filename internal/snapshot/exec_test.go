package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubLoom writes a stand-in loom script whose body is the given shell. It
// stands in for a deployed loom.sh so a test can dictate stdout, stderr and
// exit status precisely — including the combinations a real loom only produces
// when it is unhealthy.
func stubLoom(t *testing.T, body string) Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub loom script needs a POSIX shell")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "loom.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return Runner{Script: script, Dir: dir}
}

func TestRunDecodesProjection(t *testing.T) {
	runner := stubLoom(t, "cat <<'JSON'\n"+validProjection+"\nJSON\n")

	snap, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snap.Stitches) != 2 {
		t.Fatalf("got %d stitches, want 2", len(snap.Stitches))
	}
	if snap.LoomRoot != "/repo/.loom" {
		t.Errorf("loom_root = %q", snap.LoomRoot)
	}
}

// The trap this stitch exists to avoid: map exits non-zero under the same
// health rules as status, while still emitting a complete projection. A loom
// with one broken dependency must not look like a crash.
func TestRunNonZeroExitStillYieldsSnapshot(t *testing.T) {
	runner := stubLoom(t, "cat <<'JSON'\n"+validProjection+"\nJSON\n"+
		"echo 'broken dependency: second needs missing' >&2\nexit 1\n")

	snap, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if snap == nil || len(snap.Stitches) != 2 {
		t.Fatalf("got %+v, want the full projection", snap)
	}
}

// Schema drift is reported, but the snapshot still comes back so the caller
// can choose to degrade rather than fail.
func TestRunSurfacesDriftWithSnapshot(t *testing.T) {
	drifted := strings.Replace(validProjection, `"schema_version": 1`, `"schema_version": 3`, 1)
	runner := stubLoom(t, "cat <<'JSON'\n"+drifted+"\nJSON\n")

	snap, err := runner.Run(context.Background())
	if snap == nil {
		t.Fatal("want a snapshot alongside the drift")
	}
	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("got %v, want *DriftError", err)
	}
}

// A v1 loom has no map command at all. The refusal arrives on stderr with a
// non-zero exit and empty stdout, and must reach the caller intact so the
// version probe can read it.
func TestRunCapturesRefusalOnStderr(t *testing.T) {
	runner := stubLoom(t, "echo 'unknown command: map' >&2\nexit 2\n")

	snap, err := runner.Run(context.Background())
	if snap != nil {
		t.Fatalf("got snapshot %+v, want none", snap)
	}
	var execErr *ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("got %v, want *ExecError", err)
	}
	if execErr.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2", execErr.ExitCode)
	}
	if execErr.Stderr != "unknown command: map" {
		t.Errorf("stderr = %q", execErr.Stderr)
	}
	if !strings.Contains(execErr.Error(), "unknown command: map") {
		t.Errorf("error message drops stderr: %s", execErr)
	}
}

// Garbage on stdout with a zero exit is still a failure — there is no
// projection to work from.
func TestRunUnparseableStdoutFails(t *testing.T) {
	runner := stubLoom(t, "echo 'not json'\n")

	snap, err := runner.Run(context.Background())
	if snap != nil {
		t.Fatalf("got snapshot %+v, want none", snap)
	}
	var execErr *ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("got %v, want *ExecError", err)
	}
	if execErr.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", execErr.ExitCode)
	}
}

func TestRunMissingScriptFails(t *testing.T) {
	runner := Runner{Script: filepath.Join(t.TempDir(), "loom.sh")}

	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("want an error for a missing script")
	} else {
		var execErr *ExecError
		if !errors.As(err, &execErr) {
			t.Fatalf("got %v, want *ExecError", err)
		}
		if execErr.ExitCode != -1 {
			t.Errorf("exit code = %d, want -1", execErr.ExitCode)
		}
	}
}

// A slow loom must be cancellable rather than pinning a refresh forever.
func TestRunTimesOut(t *testing.T) {
	runner := stubLoom(t, "sleep 30\n")
	runner.Timeout = 50 * time.Millisecond

	start := time.Now()
	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want the timeout to cut it short", elapsed)
	}
}

func TestRunHonoursCallerCancellation(t *testing.T) {
	runner := stubLoom(t, "sleep 30\n")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if _, err := runner.Run(ctx); err == nil {
		t.Fatal("want an error once the caller cancels")
	}
}

// Only stdout is JSON: stderr chatter interleaved with a good projection must
// not corrupt the document.
func TestRunSeparatesStderrFromStdout(t *testing.T) {
	runner := stubLoom(t, "echo 'warning: stale queue record' >&2\n"+
		"cat <<'JSON'\n"+validProjection+"\nJSON\n")

	snap, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snap.Stitches) != 2 {
		t.Fatalf("got %d stitches, want 2", len(snap.Stitches))
	}
}

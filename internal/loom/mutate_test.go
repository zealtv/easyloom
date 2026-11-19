package loom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// stubLoom writes a stand-in loom script whose body is the given shell, and
// returns a Runner over it plus the path it records its argv to. It stands in
// for a deployed loom.sh so a test can dictate stdout, stderr and exit status
// precisely — including the older deployment that has no --json at all.
func stubLoom(t *testing.T, body string) (*Runner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub loom script needs a POSIX shell")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "loom.sh")
	argv := filepath.Join(dir, "argv")
	preamble := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" >> " + argv + "\n"
	if err := os.WriteFile(script, []byte(preamble+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(script, dir, knownIDs("alpha", "beta", "gamma")), argv
}

// knownIDs is a projection containing exactly these stitches, which is all the
// ID check reads.
func knownIDs(ids ...string) func() *snapshot.Snapshot {
	snap := &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
	}
	for _, id := range ids {
		snap.Stitches = append(snap.Stitches, snapshot.Stitch{ID: id, Tray: snapshot.TrayThreads})
	}
	return func() *snapshot.Snapshot { return snap }
}

func recordedArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the script never ran: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

const claimedJSON = `{"schema_version":1,"format_version":2,"command":"claim","ok":true,` +
	`"changed":true,"id":"alpha","state":"stitching","path":"threads/alpha.stitching",` +
	`"tray":"threads","queue_position":2,"completed_at":null}`

func emit(json string) string { return "cat <<'JSON'\n" + json + "\nJSON\n" }

func TestClaimDecodesTheResult(t *testing.T) {
	runner, argvPath := stubLoom(t, emit(claimedJSON))

	res, err := runner.Claim(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !res.Structured {
		t.Error("want a structured result")
	}
	if res.State != snapshot.StateStitching || res.Path != "threads/alpha.stitching" {
		t.Errorf("state = %q, path = %q", res.State, res.Path)
	}
	if !res.Queued() || *res.QueuePosition != 2 {
		t.Errorf("queue position = %v, want 2", res.QueuePosition)
	}
	if !res.Changed {
		t.Error("changed = false, want true")
	}
	// --json is recognised only before the first positional argument.
	if got, want := recordedArgv(t, argvPath), []string{"claim", "--json", "alpha"}; !equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// The trap this stitch names: before and after take the moved ID first and the
// anchor second. Backwards, they silently reorder the wrong stitch.
func TestQueueMovesPassTheMovedIDFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Runner) error
		want []string
	}{
		{"before", func(r *Runner) error {
			_, err := r.Before(context.Background(), "gamma", "alpha")
			return err
		}, []string{"before", "--json", "gamma", "alpha"}},
		{"after", func(r *Runner) error {
			_, err := r.After(context.Background(), "gamma", "alpha")
			return err
		}, []string{"after", "--json", "gamma", "alpha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, argvPath := stubLoom(t, emit(strings.Replace(claimedJSON, `"claim"`, `"`+tc.name+`"`, 1)))
			if err := tc.call(runner); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := recordedArgv(t, argvPath); !equal(got, tc.want) {
				t.Errorf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// A drop reason is one argv element however many words it contains, and the
// loom treats it literally — so a reason may itself contain --json without
// changing what the command does.
func TestDropReasonIsOneLiteralArgument(t *testing.T) {
	runner, argvPath := stubLoom(t, emit(strings.Replace(claimedJSON, `"claim"`, `"drop"`, 1)))

	if _, err := runner.Drop(context.Background(), "alpha", "superseded by --json"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	want := []string{"drop", "--json", "alpha", "superseded by --json"}
	if got := recordedArgv(t, argvPath); !equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestDropWithoutAReasonOmitsTheArgument(t *testing.T) {
	runner, argvPath := stubLoom(t, emit(strings.Replace(claimedJSON, `"claim"`, `"drop"`, 1)))

	if _, err := runner.Drop(context.Background(), "alpha", "  "); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	want := []string{"drop", "--json", "alpha"}
	if got := recordedArgv(t, argvPath); !equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// A refusal is a well-formed result, not a parse failure. The code and the
// implicated stitches reach the caller, and so does the loom's own message.
func TestRefusalCarriesTheLoomsWords(t *testing.T) {
	const refusal = `{"schema_version":1,"format_version":2,"command":"tie","ok":false,` +
		`"changed":false,"id":"alpha","error":{"code":"unresolved_children",` +
		`"message":"alpha has unresolved children: beta, gamma",` +
		`"stitch_ids":["beta","gamma"]}}`
	runner, _ := stubLoom(t, emit(refusal)+"echo 'alpha has unresolved children: beta, gamma' >&2\nexit 1\n")

	res, err := runner.Tie(context.Background(), "alpha")
	if res == nil {
		t.Fatal("want the result alongside the error")
	}
	var mutErr *Error
	if !errors.As(err, &mutErr) {
		t.Fatalf("got %v, want *Error", err)
	}
	if mutErr.Code != CodeUnresolvedChildren {
		t.Errorf("code = %q", mutErr.Code)
	}
	if !equal(mutErr.StitchIDs, []string{"beta", "gamma"}) {
		t.Errorf("stitch ids = %q", mutErr.StitchIDs)
	}
	if !strings.Contains(mutErr.Error(), "alpha has unresolved children: beta, gamma") {
		t.Errorf("error drops the loom's message: %s", mutErr)
	}
	if res.Stderr == "" {
		t.Error("stderr was not kept")
	}
}

// The code set is open. An unrecognised one is a code this package has not
// heard of, not a schema that moved.
func TestUnrecognisedCodeIsNotDrift(t *testing.T) {
	const refusal = `{"schema_version":1,"format_version":2,"command":"claim","ok":false,` +
		`"changed":false,"id":"alpha","error":{"code":"moon_phase","message":"no","stitch_ids":[]}}`
	runner, _ := stubLoom(t, emit(refusal)+"exit 1\n")

	res, err := runner.Claim(context.Background(), "alpha")
	var mutErr *Error
	if !errors.As(err, &mutErr) {
		t.Fatalf("got %v, want *Error", err)
	}
	if mutErr.Code != "moon_phase" {
		t.Errorf("code = %q, want it passed through", mutErr.Code)
	}
	if res.Drift != nil {
		t.Errorf("unrecognised code reported as drift: %v", res.Drift)
	}
}

// Drift means the schema moved. The result still decodes, because a viewer
// that stops working on a loom that gained a field is worse than one that
// reads the fields it knows.
func TestDriftAccompaniesAUsableResult(t *testing.T) {
	drifted := strings.Replace(claimedJSON, `"schema_version":1`, `"schema_version":2,"weather":"fine"`, 1)
	runner, _ := stubLoom(t, emit(drifted))

	res, err := runner.Claim(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.Drift == nil {
		t.Fatal("want drift reported")
	}
	if res.State != snapshot.StateStitching {
		t.Errorf("state = %q, want the result still decoded", res.State)
	}
	if !strings.Contains(res.Drift.Error(), "weather") ||
		!strings.Contains(res.Drift.Error(), "schema_version 2") {
		t.Errorf("drift = %v, want both issues", res.Drift)
	}
}

// A deployed script older than --json reads the flag as a stitch ID and
// refuses before changing anything, so the prose path is safe to fall back to
// — and a loom's own loom.sh is its protocol version, so both are in the wild.
func TestFallsBackToProseForAnOlderScript(t *testing.T) {
	runner, argvPath := stubLoom(t, `
if [[ "$2" == --json ]]; then
  echo "invalid stitch id '--json'" >&2
  exit 1
fi
echo "claimed $2"
`)

	res, err := runner.Claim(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.Structured {
		t.Error("want a prose result")
	}
	if res.Stdout != "claimed alpha" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.ID != "alpha" {
		t.Errorf("id = %q", res.ID)
	}

	// One refusal is enough: the second mutation goes straight to prose.
	if _, err := runner.Claim(context.Background(), "beta"); err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	want := []string{"claim", "--json", "alpha", "claim", "alpha", "claim", "beta"}
	if got := recordedArgv(t, argvPath); !equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// A prose failure is still a failure, and the loom's stderr is still the
// explanation.
func TestProseFailureSurfacesStderr(t *testing.T) {
	runner, _ := stubLoom(t, "echo 'stale queue record: ghost' >&2\nexit 1\n")

	_, err := runner.Claim(context.Background(), "alpha")
	var mutErr *Error
	if !errors.As(err, &mutErr) {
		t.Fatalf("got %v, want *Error", err)
	}
	if mutErr.Code != "" {
		t.Errorf("code = %q, want none from a prose refusal", mutErr.Code)
	}
	if !strings.Contains(mutErr.Error(), "stale queue record: ghost") {
		t.Errorf("error drops stderr: %s", mutErr)
	}
}

// A command that exits zero worked, whatever it said. It is never run again to
// find out why the result was unreadable — it has already done whatever it did.
func TestUnreadableSuccessIsNotRetried(t *testing.T) {
	runner, argvPath := stubLoom(t, "echo 'claimed alpha'\n")

	res, err := runner.Claim(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.Structured {
		t.Error("want a prose result")
	}
	want := []string{"claim", "--json", "alpha"}
	if got := recordedArgv(t, argvPath); !equal(got, want) {
		t.Errorf("argv = %q, want one run", got)
	}
}

func TestUnknownIDNeverRunsAnything(t *testing.T) {
	runner, argvPath := stubLoom(t, emit(claimedJSON))

	_, err := runner.Claim(context.Background(), "not-a-stitch")
	var idErr *IDError
	if !errors.As(err, &idErr) {
		t.Fatalf("got %v, want *IDError", err)
	}
	if _, err := os.Stat(argvPath); err == nil {
		t.Error("the script ran for an ID that is not in the loom")
	}
}

// A loom is a directory tree somebody else can write to. argv keeps a dash out
// of the shell but not out of the loom's own option parsing.
func TestRefusesIDsThatAreNotStitchIDs(t *testing.T) {
	for _, id := range []string{"", "--json", "-alpha", "alpha beta", "alpha;rm -rf /", "../escape"} {
		runner, argvPath := stubLoom(t, emit(claimedJSON))
		_, err := runner.Wait(context.Background(), id)
		var idErr *IDError
		if !errors.As(err, &idErr) {
			t.Errorf("%q: got %v, want *IDError", id, err)
		}
		if _, err := os.Stat(argvPath); err == nil {
			t.Errorf("%q: the script ran", id)
		}
	}
}

// The anchor is a stitch ID too, and gets the same treatment.
func TestRefusesAnUnknownAnchor(t *testing.T) {
	runner, _ := stubLoom(t, emit(claimedJSON))

	_, err := runner.Before(context.Background(), "alpha", "-anchor")
	var idErr *IDError
	if !errors.As(err, &idErr) {
		t.Fatalf("got %v, want *IDError", err)
	}
}

// With no projection there is nothing to check against. The check exists to
// turn a confusing loom error into a clear one, not to be the authority — the
// loom still has the last word.
func TestNoSnapshotDoesNotBlockAMutation(t *testing.T) {
	runner, _ := stubLoom(t, emit(claimedJSON))
	runner.Snapshot = func() *snapshot.Snapshot { return nil }

	if _, err := runner.Claim(context.Background(), "unknown-but-valid"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
}

func TestMissingScriptFailsWithoutRetrying(t *testing.T) {
	runner := New(filepath.Join(t.TempDir(), "loom.sh"), t.TempDir(), nil)

	_, err := runner.Claim(context.Background(), "alpha")
	var mutErr *Error
	if !errors.As(err, &mutErr) {
		t.Fatalf("got %v, want *Error", err)
	}
	if mutErr.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", mutErr.ExitCode)
	}
}

func TestCancellationStopsTheCommand(t *testing.T) {
	runner, _ := stubLoom(t, "sleep 30\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Claim(ctx, "alpha"); err == nil {
		t.Fatal("want an error once the caller cancels")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unqueue is the repair path: the protocol accepts any syntactically valid ID
// for it so a manually stale queue record can be removed. Refusing one here
// would leave the only command that fixes a stale queue unreachable — and a
// stale record makes every other queue mutation fail without changing the file.
func TestUnqueueReachesAnIDTheProjectionDoesNotHave(t *testing.T) {
	runner, argvPath := stubLoom(t, emit(unqueuedJSON))

	if _, err := runner.Unqueue(context.Background(), "ghost-stitch"); err != nil {
		t.Fatalf("unqueue was refused: %v", err)
	}
	if got := recordedArgv(t, argvPath); !equal(got, []string{"unqueue", "--json", "ghost-stitch"}) {
		t.Errorf("ran %v", got)
	}

	// The shape check still applies, whichever command it is.
	runner, argvPath = stubLoom(t, emit(unqueuedJSON))
	if _, err := runner.Unqueue(context.Background(), "alpha;rm -rf /"); err == nil {
		t.Error("unqueue accepted an ID that is not a stitch ID")
	}
	if _, err := os.Stat(argvPath); err == nil {
		t.Error("the script ran for an ID that is not a stitch ID")
	}

	// And every other queue command still refuses an ID the loom does not have.
	runner, _ = stubLoom(t, emit(unqueuedJSON))
	if _, err := runner.Queue(context.Background(), "ghost-stitch"); err == nil {
		t.Error("queue accepted an ID the projection does not have")
	}
}

const unqueuedJSON = `{"schema_version":1,"format_version":2,"command":"unqueue","ok":true,` +
	`"changed":true,"id":"ghost-stitch","state":"","path":"","tray":"",` +
	`"queue_position":null,"completed_at":null}`

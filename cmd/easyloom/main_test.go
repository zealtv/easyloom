package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/target"
)

// projection is a stub map document with one queued stitch, so the frame has
// something to draw.
const projection = `{
  "schema_version": 1,
  "format_version": 2,
  "loom_root": "%ROOT%",
  "stitches": [
    {
      "id": "first", "root_id": "goal", "parent_id": "goal",
      "path": "threads/goal/first", "tray": "threads", "state": "plain",
      "ready": true, "waiting_inherited": false, "queue_position": 1,
      "completed_at": null, "archived": false, "legacy": false,
      "children": [], "dependencies": [], "cycle": []
    }
  ],
  "decomposition_edges": [],
  "dependency_edges": [],
  "cycles": [],
  "frontier": ["first"],
  "recently_completed": [],
  "diagnostics": []
}`

// stubLoom deploys a loom at <dir>/.loom whose map behaviour the test dictates.
// A body of "" means a working v2 loom emitting the projection above.
func stubLoom(t *testing.T, marker, body string) (root, loomDir string) {
	t.Helper()
	root = t.TempDir()
	// The temp dir may sit under a symlink, and the resolved target will not.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	loomDir = filepath.Join(root, ".loom")
	if err := os.MkdirAll(loomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		doc := strings.ReplaceAll(projection, "%ROOT%", loomDir)
		body = "cat <<'JSON'\n" + doc + "\nJSON\n"
	}
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(loomDir, "format-version"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, loomDir
}

// testConfigHome is a throwaway user configuration directory for this whole
// package. run() records every loom it opens in the registry, so a suite that
// let that fall through to os.UserHomeDir would write the developer's real
// ~/.config/easyloom full of temp-directory paths — which is exactly what it
// did until this existed.
var testConfigHome string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "easyloom-config-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "test config home: %v\n", err)
		os.Exit(1)
	}
	testConfigHome = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// noEnv is an empty environment except for the one variable that decides where
// configuration is written. Tests that care about $LOOM_DIR wrap it.
func noEnv(key string) string {
	if key == "XDG_CONFIG_HOME" {
		return testConfigHome
	}
	return ""
}

// withTestConfigHome forces $XDG_CONFIG_HOME onto whatever environment a test
// supplies. It is here rather than in each test's own getenv so that a new
// test cannot forget it: the cost of forgetting is writing to the developer's
// real configuration directory, which no test failure would ever reveal.
func withTestConfigHome(getenv func(string) string) func(string) string {
	return func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return testConfigHome
		}
		return getenv(key)
	}
}

// exercise runs the command and returns stdout, stderr and the error.
func exercise(t *testing.T, args []string, getenv func(string) string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr, withTestConfigHome(getenv))
	return stdout.String(), stderr.String(), err
}

func TestRunExplicitPath(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{loomDir, "via the command line", "tier     v2", "FRONTIER", "first"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// The trust note: the script about to be executed is named before it runs.
func TestRunPrintsResolvedScript(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(loomDir, "loom.sh")
	if !strings.Contains(stdout, script) {
		t.Fatalf("output does not name the script it ran:\n%s", stdout)
	}
	if strings.Index(stdout, script) > strings.Index(stdout, "tier") {
		t.Error("the script is named after it was run, not before")
	}
}

// Resolution order, first match wins.
func TestRunPrefersArgumentOverEnvironment(t *testing.T) {
	argRoot, argLoom := stubLoom(t, "2\n", "")
	_, envLoom := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, []string{argRoot}, func(string) string { return envLoom })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, argLoom) || strings.Contains(stdout, envLoom) {
		t.Fatalf("argument did not win over $LOOM_DIR:\n%s", stdout)
	}
}

func TestRunUsesLoomDirEnvironment(t *testing.T) {
	_, loomDir := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, nil, func(name string) string {
		if name == "LOOM_DIR" {
			return loomDir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "via $LOOM_DIR") {
		t.Fatalf("output does not credit $LOOM_DIR:\n%s", stdout)
	}
}

func TestRunWalksUpFromWorkingDirectory(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, nested)

	stdout, _, err := exercise(t, nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, loomDir) || !strings.Contains(stdout, "via the working directory") {
		t.Fatalf("walk up did not find the loom:\n%s", stdout)
	}
}

// Nothing found: say what was looked for and where, and exit non-zero.
func TestRunNoLoomFoundReportsSearch(t *testing.T) {
	empty := t.TempDir()
	chdir(t, empty)

	_, _, err := exercise(t, nil, noEnv)
	if err == nil {
		t.Fatal("want an error when no loom is found")
	}
	message := err.Error()
	if !strings.Contains(message, "loom.sh") || !strings.Contains(message, "no loom found") {
		t.Errorf("error does not say what was searched for: %v", err)
	}
}

// A v1 loom degrades to read-only rather than erroring out.
func TestRunV1DegradesReadOnly(t *testing.T) {
	root, _ := stubLoom(t, "", "echo 'error: map requires a format v2 loom (run migrate-v2 first)' >&2\nexit 1\n")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatalf("run: %v, want a clean exit for a v1 loom", err)
	}
	for _, want := range []string{"v1 — read-only", "migrate-v2 --dry-run", "no map command"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// An unreadable schema is refused, naming the version seen.
func TestRunUnsupportedSchemaFails(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")
	doc := strings.ReplaceAll(strings.Replace(projection, `"schema_version": 1`, `"schema_version": 9`, 1), "%ROOT%", loomDir)
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"),
		[]byte("#!/usr/bin/env bash\ncat <<'JSON'\n"+doc+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err == nil {
		t.Fatal("want an error for an unsupported schema")
	}
	if !strings.Contains(err.Error(), "9") {
		t.Errorf("error does not name the schema seen: %v", err)
	}
	if !strings.Contains(stdout, "unsupported") {
		t.Errorf("output does not report the tier:\n%s", stdout)
	}
}

// A broken loom that still emits a projection is not a crash: map exits
// non-zero under the same health rules as status, and run reports no error and
// draws the projection anyway. (The diagnostics themselves are not asserted:
// the TUI frame this test reads does not print them.)
func TestRunUnhealthyLoomStillReports(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")
	doc := strings.ReplaceAll(projection, "%ROOT%", loomDir)
	doc = strings.Replace(doc, `"diagnostics": []`,
		`"diagnostics": [{"severity": "error", "code": "broken-dependency", "message": "first needs missing", "stitch_id": "first", "target_id": "missing"}]`, 1)
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"),
		[]byte("#!/usr/bin/env bash\ncat <<'JSON'\n"+doc+"\nJSON\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatalf("run: %v, want the projection used despite a non-zero exit", err)
	}
	if !strings.Contains(stdout, "FRONTIER") {
		t.Errorf("the TUI did not draw the projection:\n%s", stdout)
	}
}

// The probe already paid for a snapshot; startup must not immediately take a
// second one.
func TestRunSnapshotsOnce(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")
	counter := filepath.Join(t.TempDir(), "runs")
	doc := strings.ReplaceAll(projection, "%ROOT%", loomDir)
	body := "echo x >> " + counter + "\ncat <<'JSON'\n" + doc + "\nJSON\n"
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := exercise(t, []string{root}, noEnv); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if runs := strings.Count(string(data), "\n"); runs != 1 {
		t.Fatalf("map ran %d times at startup, want 1", runs)
	}
}

func TestRunVersionFlag(t *testing.T) {
	stdout, _, err := exercise(t, []string{"--version"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("--version printed nothing")
	}
}

func TestRunRejectsTwoPaths(t *testing.T) {
	if _, _, err := exercise(t, []string{"a", "b"}, noEnv); err == nil {
		t.Fatal("want an error for two paths")
	}
}

// chdir changes directory for the duration of a test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

// The terminal UI is what runs. Redirected output is not a
// terminal, so it gets one plain frame rather than an interactive program with
// no keyboard.
func TestRunStartsTheTUIByDefault(t *testing.T) {
	root, _ := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"FRONTIER", "1 stitches", ": commands"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("frame missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("non-terminal output carries escape sequences:\n%q", stdout)
	}
}

// The trust note holds for the TUI too: the script is named before the probe
// executes it, and it stays on stdout above the frame.
func TestRunTUINamesTheScriptFirst(t *testing.T) {
	root, loomDir := stubLoom(t, "2\n", "")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(loomDir, "loom.sh")
	if !strings.Contains(stdout, script) {
		t.Fatalf("output does not name the script it ran:\n%s", stdout)
	}
	if strings.Index(stdout, script) > strings.Index(stdout, "FRONTIER") {
		t.Error("the script is named after the frontier was drawn, not before")
	}
}

// A v1 loom has no projection, so the full UI never starts.
func TestRunV1DoesNotStartTheTUI(t *testing.T) {
	root, _ := stubLoom(t, "", "echo 'error: map requires a format v2 loom (run migrate-v2 first)' >&2\nexit 1\n")

	stdout, _, err := exercise(t, []string{root}, noEnv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(stdout, "FRONTIER") {
		t.Errorf("the TUI started for a v1 loom:\n%s", stdout)
	}
	if !strings.Contains(stdout, "migrate-v2 --dry-run") {
		t.Errorf("output missing the migration hint:\n%s", stdout)
	}
}

func TestRunScanListsLoomsWithTiers(t *testing.T) {
	// A scan is pointed at a tree and reports the tier of every loom in it.
	// stubLoom builds one loom per temp dir, so each tier is exercised by
	// scanning its own repository root — the walk itself is covered in
	// internal/target.
	_, v2Loom := stubLoom(t, "2\n", "")
	_, v1Loom := stubLoom(t, "", "echo 'unknown command: map' >&2; exit 1")

	for _, loom := range []struct {
		dir  string
		tier string
	}{{v2Loom, "v2"}, {v1Loom, "v1"}} {
		stdout, _, err := exercise(t, []string{"--scan", filepath.Dir(loom.dir)}, noEnv)
		if err != nil {
			t.Fatalf("--scan %s: %v", loom.dir, err)
		}
		if !strings.Contains(stdout, "found    1 loom\n") {
			t.Errorf("--scan %s reported:\n%s", loom.dir, stdout)
		}
		if !strings.Contains(stdout, loom.tier) || !strings.Contains(stdout, loom.dir) {
			t.Errorf("--scan %s did not report %s at %s:\n%s", loom.dir, loom.tier, loom.dir, stdout)
		}
	}
}

func TestRunScanReportsAnEmptyTree(t *testing.T) {
	stdout, _, err := exercise(t, []string{"--scan", t.TempDir()}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "found    no looms") {
		t.Errorf("--scan of an empty tree reported:\n%s", stdout)
	}
}

// --scan answers a question and exits; it never opens the UI, so it does
// not also take the path one would be opened from.
func TestRunScanRejectsAPathAsWell(t *testing.T) {
	_, _, err := exercise(t, []string{"--scan", t.TempDir(), t.TempDir()}, noEnv)
	if err == nil {
		t.Fatal("--scan with a path = nil error, want one")
	}
	if !strings.Contains(err.Error(), "--scan") {
		t.Errorf("error = %v, want it to name --scan", err)
	}
}

func TestRunScanReportsAMissingDirectory(t *testing.T) {
	_, _, err := exercise(t, []string{"--scan", filepath.Join(t.TempDir(), "absent")}, noEnv)
	if err == nil {
		t.Fatal("--scan of a missing directory = nil error, want one")
	}
}

// Opening a loom records it, so the picker can offer it next time. This also
// proves the suite's configuration redirect works: without it this assertion
// would pass while writing to the developer's real home.
func TestRunRemembersTheLoomItOpened(t *testing.T) {
	_, loomDir := stubLoom(t, "2\n", "")

	if _, _, err := exercise(t, []string{loomDir}, noEnv); err != nil {
		t.Fatal(err)
	}

	list, err := known.Load(withTestConfigHome(noEnv))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(list.Path, testConfigHome) {
		t.Fatalf("the registry was written to %s, outside the test config home", list.Path)
	}
	var found bool
	for _, l := range list.Looms {
		if l.Root == loomDir {
			found = true
			if l.Tier != target.TierV2 {
				t.Errorf("tier = %q, want v2", l.Tier)
			}
		}
	}
	if !found {
		t.Errorf("the registry does not hold %s", loomDir)
	}
}

// A v1 loom is still one the user opened and will want to find again.
func TestRunRemembersAV1Loom(t *testing.T) {
	_, loomDir := stubLoom(t, "", "echo 'unknown command: map' >&2; exit 1")

	if _, _, err := exercise(t, []string{loomDir}, noEnv); err != nil {
		t.Fatal(err)
	}

	list, err := known.Load(withTestConfigHome(noEnv))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list.Looms {
		if l.Root == loomDir {
			if l.Tier != target.TierV1 {
				t.Errorf("tier = %q, want v1", l.Tier)
			}
			return
		}
	}
	t.Errorf("the registry does not hold the v1 loom %s", loomDir)
}

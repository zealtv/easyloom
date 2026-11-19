package target

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixtureLoom deploys a stand-in script at dir/<name>. Resolution only cares
// that the deployed script exists and is executable, so a stub is enough here;
// stitches that actually run loom need a real fixture loom.
func fixtureLoom(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, name)
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// chdir changes directory for the duration of a test. t.Chdir would do this,
// but it needs Go 1.24 and the module targets 1.22.
func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	})
}

// repoWithLoom builds <root>/.loom/loom.sh plus a nested working directory.
func repoWithLoom(t *testing.T) (root, loomDir, script string) {
	t.Helper()
	// The temp dir itself may sit under a symlink (/tmp -> /private/tmp on
	// macOS), so compare against its resolved form.
	root = t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	loomDir = filepath.Join(root, ".loom")
	script = fixtureLoom(t, loomDir, "loom.sh")
	if err := os.MkdirAll(filepath.Join(root, "python", "osc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, loomDir, script
}

func TestResolveAcceptsEveryForm(t *testing.T) {
	root, loomDir, script := repoWithLoom(t)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"repo root", root},
		{"loom dir", loomDir},
		{"script", script},
		{"nested dir walks up", filepath.Join(root, "python", "osc")},
		{"nested file walks up", filepath.Join(loomDir, "queue")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "nested file walks up" {
				if err := os.WriteFile(tc.path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Resolve(tc.path)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.path, err)
			}
			if got.Script != script {
				t.Errorf("Script = %q, want %q", got.Script, script)
			}
			if got.Root != loomDir {
				t.Errorf("Root = %q, want %q", got.Root, loomDir)
			}
		})
	}
}

func TestResolveDefaultsToWorkingDirectory(t *testing.T) {
	root, loomDir, script := repoWithLoom(t)
	chdir(t, filepath.Join(root, "python", "osc"))

	got, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve(\"\"): %v", err)
	}
	if got.Script != script || got.Root != loomDir {
		t.Errorf("Resolve(\"\") = %+v, want script %q root %q", got, script, loomDir)
	}
}

func TestResolveFindsV1ScriptName(t *testing.T) {
	root := t.TempDir()
	script := fixtureLoom(t, filepath.Join(root, ".loom"), "loom")

	got, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(got.Script) != "loom" {
		t.Errorf("Script = %q, want the v1 name", got.Script)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePrefersV2ScriptName(t *testing.T) {
	root := t.TempDir()
	loomDir := filepath.Join(root, ".loom")
	fixtureLoom(t, loomDir, "loom")
	fixtureLoom(t, loomDir, "loom.sh")

	got, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(got.Script) != "loom.sh" {
		t.Errorf("Script = %q, want loom.sh", got.Script)
	}
}

func TestResolveReturnsNearestLoom(t *testing.T) {
	root, _, _ := repoWithLoom(t)
	inner := filepath.Join(root, "python")
	innerScript := fixtureLoom(t, filepath.Join(inner, ".loom"), "loom.sh")

	got, err := Resolve(filepath.Join(inner, "osc"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Script != innerScript {
		t.Errorf("Script = %q, want the nearest loom %q", got.Script, innerScript)
	}
}

func TestResolveSymlinkedPathsGiveOneKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root, _, script := repoWithLoom(t)

	link := filepath.Join(t.TempDir(), "link-to-repo")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	direct, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := Resolve(link)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Key() != viaLink.Key() {
		t.Errorf("keys differ: %q vs %q", direct.Key(), viaLink.Key())
	}
	if viaLink.Script != script {
		t.Errorf("Script = %q, want the realpath %q", viaLink.Script, script)
	}
}

func TestResolveNoLoomSaysWhatItLookedFor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(dir)
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	msg := notFound.Error()
	for _, want := range []string{".loom/loom.sh", ".loom/loom", dir, string(filepath.Separator)} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if len(notFound.Searched) < 2 || notFound.Searched[0] != dir {
		t.Errorf("Searched = %v, want the walk starting at %q", notFound.Searched, dir)
	}
	if last := notFound.Searched[len(notFound.Searched)-1]; last != filepath.Dir(last) {
		t.Errorf("walk stopped at %q, want the filesystem root", last)
	}
}

func TestResolveMissingPath(t *testing.T) {
	_, err := Resolve(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("Resolve accepted a path that does not exist")
	}
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		t.Error("a missing path reported as no-loom-found rather than as a bad path")
	}
}

func TestResolveNonExecutableScript(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the executable bit")
	}
	root := t.TempDir()
	loomDir := filepath.Join(root, ".loom")
	if err := os.MkdirAll(loomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(root)
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("err = %v, want a not-executable error", err)
	}
}

func TestResolveRealRepoLoom(t *testing.T) {
	// This repo carries its own v2 loom, so resolution can be checked against
	// a real deployment. It is a live fixture and may change; assert only the
	// shape.
	got, err := Resolve("../..")
	if err != nil {
		t.Skipf("no loom above the package directory: %v", err)
	}
	if filepath.Base(got.Script) != "loom.sh" {
		t.Errorf("Script = %q", got.Script)
	}
	if filepath.Base(got.Root) != ".loom" {
		t.Errorf("Root = %q", got.Root)
	}
	if !filepath.IsAbs(got.Script) {
		t.Errorf("Script %q is not absolute", got.Script)
	}
}

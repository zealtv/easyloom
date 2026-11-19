package userconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env builds a Getenv over a fixed map, so a test states the whole environment
// it cares about and inherits none of the real one.
func env(pairs map[string]string) Getenv {
	return func(key string) string { return pairs[key] }
}

func TestDirPrefersAbsoluteXDGConfigHome(t *testing.T) {
	got, err := Dir(env(map[string]string{
		"XDG_CONFIG_HOME": "/xdg",
		"HOME":            "/home/someone",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestDirFallsBackToHomeConfig(t *testing.T) {
	got, err := Dir(env(map[string]string{"HOME": "/home/someone"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/someone", ".config", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// A relative XDG_CONFIG_HOME is ignored, not joined to the working directory:
// the configuration directory must not move with cd.
func TestDirIgnoresRelativeXDGConfigHome(t *testing.T) {
	got, err := Dir(env(map[string]string{
		"XDG_CONFIG_HOME": "relative/config",
		"HOME":            "/home/someone",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/someone", ".config", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// An empty XDG_CONFIG_HOME is the unset case; the specification says so
// explicitly and it is what an exported-but-empty shell variable produces.
func TestDirTreatsEmptyXDGConfigHomeAsUnset(t *testing.T) {
	got, err := Dir(env(map[string]string{
		"XDG_CONFIG_HOME": "",
		"HOME":            "/home/someone",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/someone", ".config", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestFileJoinsNameOntoDir(t *testing.T) {
	got, err := File(env(map[string]string{"XDG_CONFIG_HOME": "/xdg"}), "registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg", "easyloom", "registry.json"); got != want {
		t.Errorf("File = %q, want %q", got, want)
	}
}

func TestFileRejectsPaths(t *testing.T) {
	for _, name := range []string{"", "sub/registry.json", "../escape.json"} {
		if _, err := File(env(map[string]string{"XDG_CONFIG_HOME": "/xdg"}), name); err == nil {
			t.Errorf("File(%q) = nil error, want one", name)
		}
	}
}

// Resolving is not creating. The picker asks where the registry would be on
// every start, including on machines that will never have one.
func TestDirAndFileCreateNothing(t *testing.T) {
	root := t.TempDir()
	getenv := env(map[string]string{"XDG_CONFIG_HOME": root})

	if _, err := Dir(getenv); err != nil {
		t.Fatal(err)
	}
	if _, err := File(getenv, "registry.json"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("resolving created %d entries under %s, want none", len(entries), root)
	}
}

func TestEnsureCreatesTheDirectory(t *testing.T) {
	root := t.TempDir()
	getenv := env(map[string]string{"XDG_CONFIG_HOME": root})

	dir, err := Ensure(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "easyloom"); dir != want {
		t.Fatalf("Ensure = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}

	// Ensure is called before every write, so it has to be idempotent.
	if _, err := Ensure(getenv); err != nil {
		t.Errorf("second Ensure: %v", err)
	}
}

// With no absolute HOME and no XDG_CONFIG_HOME, resolution falls through to
// the toolchain's own home lookup rather than inventing a relative path.
func TestDirFallsBackToUserHomeDir(t *testing.T) {
	got, err := Dir(env(nil))
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		if err == nil {
			t.Fatalf("Dir = %q, want an error matching os.UserHomeDir: %v", got, homeErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// A nil Getenv reads the real environment, so callers with nothing to inject
// need not build one.
func TestNilGetenvReadsTheEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg-from-env")

	got, err := Dir(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg-from-env", "easyloom"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "easyloom") {
		t.Errorf("Dir = %q, want it to end in the application directory", got)
	}
}

package target

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scriptNames are the names a deployed loom script can have, in preference
// order: v2 installs loom.sh, the v1 installer named it loom.
var scriptNames = []string{"loom.sh", "loom"}

// loomDirName is the directory a loom is deployed into inside a repository.
const loomDirName = ".loom"

// Target is a resolved loom.
//
// Script is the loom's address. Each .loom carries its own copy of the script
// and that copy is the protocol version for that loom, so it is always
// invoked by this absolute path — never resolved from $PATH, never substituted
// with a copy of our own.
type Target struct {
	// Script is the absolute, symlink-resolved path to the deployed script.
	Script string
	// Root is the absolute path to the loom directory the script operates on,
	// which is the directory the script lives in.
	Root string
}

// Key identifies the loom. Two paths that reach the same loom produce the same
// key, so a registry never ends up with two entries and two watchers for one
// loom.
func (t Target) Key() string { return t.Script }

// NotFoundError reports that no loom was found, and says exactly what was
// looked for and where.
type NotFoundError struct {
	// Start is the absolute path resolution started from.
	Start string
	// Searched lists the directories walked, nearest first.
	Searched []string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no loom found from %s: looked for %s in %s",
		e.Start,
		strings.Join(prefixed(loomDirName+string(filepath.Separator), scriptNames), " or "),
		strings.Join(e.Searched, ", "))
}

func prefixed(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}

// Resolve turns any reasonable path into a loom target. It accepts a
// repository root, the loom directory itself, the script, or anything nested
// inside the repository, in which case it walks up looking for a loom the way
// git looks for .git. An empty path means the current directory.
//
// A repository may legitimately hold more than one loom, since each copy is
// self-contained; the walk returns the nearest.
func Resolve(path string) (Target, error) {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Target{}, fmt.Errorf("resolve loom: current directory: %w", err)
		}
		path = cwd
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return Target{}, fmt.Errorf("resolve loom: %q: %w", path, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return Target{}, fmt.Errorf("resolve loom: %q: %w", path, err)
	}

	// The path names the script itself.
	if !info.IsDir() {
		if isScriptName(filepath.Base(abs)) {
			return newTarget(abs)
		}
		// Some other file: search from the directory holding it.
		abs = filepath.Dir(abs)
	}

	// The path is a loom directory: it holds the script directly.
	if script, ok := scriptIn(abs); ok {
		return newTarget(script)
	}

	// Otherwise walk up looking for <dir>/.loom/<script>, git-style.
	var searched []string
	for dir := abs; ; {
		searched = append(searched, dir)
		if script, ok := scriptIn(filepath.Join(dir, loomDirName)); ok {
			return newTarget(script)
		}
		parent := filepath.Dir(dir)
		if parent == dir { // filesystem root
			break
		}
		dir = parent
	}

	return Target{}, &NotFoundError{Start: abs, Searched: searched}
}

func isScriptName(name string) bool {
	for _, candidate := range scriptNames {
		if name == candidate {
			return true
		}
	}
	return false
}

// scriptIn returns the deployed script inside dir, preferring the v2 name.
func scriptIn(dir string) (string, bool) {
	for _, name := range scriptNames {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

// newTarget normalises to the realpath of the script and uses that as the key,
// so the same loom reached two ways is one target.
func newTarget(script string) (Target, error) {
	real, err := filepath.EvalSymlinks(script)
	if err != nil {
		return Target{}, fmt.Errorf("resolve loom script %q: %w", script, err)
	}

	info, err := os.Stat(real)
	if err != nil {
		return Target{}, fmt.Errorf("resolve loom script %q: %w", script, err)
	}
	if !info.Mode().IsRegular() {
		return Target{}, fmt.Errorf("resolve loom script %q: not a regular file", real)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Target{}, fmt.Errorf("resolve loom script %q: not executable", real)
	}

	return Target{Script: real, Root: filepath.Dir(real)}, nil
}

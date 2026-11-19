package known

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// FileName is the registry's name inside the user configuration directory.
const FileName = "registry.json"

// fileVersion is the on-disk shape's version. It is written so that a future
// change has something to branch on; a file carrying any other value is read
// as no registry at all, which costs the user one scan.
const fileVersion = 1

// maxEntries caps the file. A registry is a convenience, and an unbounded list
// of every loom ever opened is a picker nobody can find anything in.
const maxEntries = 50

// now is the clock, replaced in tests. MRU order is the whole point of this
// file, so the ordering tests cannot be at the mercy of a timer resolution.
var now = time.Now

// Loom is one remembered loom.
type Loom struct {
	// Key is the realpath of the deployed script — target.Target.Key(). Two
	// paths that reach one loom produce one entry.
	Key string `json:"key"`
	// Root is the loom directory, which is what a picker shows.
	Root string `json:"root"`
	// LastOpened orders the list.
	LastOpened time.Time `json:"last_opened"`
	// Tier is the last tier this loom was seen at, cached for display only.
	// The picker draws it immediately and the probe corrects it: a registry
	// that must probe every entry before it can draw is a registry that made
	// startup slower.
	Tier target.Tier `json:"tier,omitempty"`
}

// Target rebuilds the resolved target an entry stands for. The key is the
// script's realpath and the root is the directory it lives in, so no
// filesystem access is needed — and none should be, since an entry may name a
// loom that is not currently reachable.
func (l Loom) Target() target.Target {
	return target.Target{Script: l.Key, Root: l.Root}
}

// List is a registry as loaded, in most-recently-used order.
type List struct {
	// Path is where it was read from, for messages.
	Path string
	// Looms is the list, newest first.
	Looms []Loom
}

// file is the on-disk shape. The version lives beside the entries rather than
// being inferred from them.
type file struct {
	Version int    `json:"version"`
	Looms   []Loom `json:"looms"`
}

// Load reads the registry.
//
// It always returns a usable List. A missing file is an empty registry and no
// error at all — the normal state on a machine that has never opened a second
// loom. A corrupt or unreadable one is an empty registry and an error the
// caller should report as a warning and then carry on: nothing here cannot be
// rebuilt by target.Scan, and refusing to start over a broken cache of paths
// would be a poor trade.
func Load(getenv userconfig.Getenv) (*List, error) {
	path, err := userconfig.File(getenv, FileName)
	if err != nil {
		return &List{}, err
	}
	list := &List{Path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return list, fmt.Errorf("read %s: %w", path, err)
	}

	var decoded file
	if err := json.Unmarshal(data, &decoded); err != nil {
		return list, fmt.Errorf("read %s: %w", path, err)
	}
	if decoded.Version != fileVersion {
		return list, fmt.Errorf("read %s: version %d, and this build writes version %d",
			path, decoded.Version, fileVersion)
	}

	list.Looms = clean(decoded.Looms)
	return list, nil
}

// Touch records that a loom has been opened, moving it to the front, and
// rewrites the registry.
//
// It re-reads the file first rather than saving a list held from earlier. Two
// EasyLooms open at once is the normal case, not the exotic one, and a
// read-modify-write over the current file loses at most a simultaneous touch
// rather than every entry the other process has added since.
func Touch(getenv userconfig.Getenv, t target.Target, tier target.Tier) error {
	// A load error here is the corrupt-file case, and the response to it is to
	// replace the file rather than to refuse to record anything ever again.
	list, _ := Load(getenv)

	entry := Loom{Key: t.Key(), Root: t.Root, LastOpened: now(), Tier: tier}
	looms := make([]Loom, 0, len(list.Looms)+1)
	looms = append(looms, entry)
	for _, existing := range list.Looms {
		if existing.Key == entry.Key {
			continue
		}
		looms = append(looms, existing)
	}

	return save(getenv, looms)
}

// Forget drops a loom from the registry. A picker needs it: an entry whose
// loom has been deleted is otherwise permanent.
func Forget(getenv userconfig.Getenv, key string) error {
	list, _ := Load(getenv)

	looms := make([]Loom, 0, len(list.Looms))
	for _, existing := range list.Looms {
		if existing.Key == key {
			continue
		}
		looms = append(looms, existing)
	}
	if len(looms) == len(list.Looms) {
		return nil
	}
	return save(getenv, looms)
}

// save writes the registry atomically: a temporary file in the same directory,
// then a rename. A reader therefore sees the old file or the new one, never a
// half-written one — which matters because a half-written registry must never
// be the thing that stops the next EasyLoom starting.
func save(getenv userconfig.Getenv, looms []Loom) error {
	dir, err := userconfig.Ensure(getenv)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)

	if len(looms) > maxEntries {
		looms = looms[:maxEntries]
	}
	data, err := json.MarshalIndent(file{Version: fileVersion, Looms: looms}, "", "  ")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	data = append(data, '\n')

	// Same directory, so the rename is within one filesystem and is therefore
	// atomic. A unique name, so two processes saving at once do not share a
	// temporary file.
	temp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(temp.Name(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// clean drops entries a hand-edited or truncated file may carry — no key, or a
// key repeated — and puts the rest in MRU order. The file is written in order,
// but it is a file in a home directory and may have been edited by hand.
func clean(looms []Loom) []Loom {
	seen := map[string]bool{}
	out := make([]Loom, 0, len(looms))
	for _, l := range looms {
		if l.Key == "" || l.Root == "" || seen[l.Key] {
			continue
		}
		seen[l.Key] = true
		out = append(out, l)
	}
	sortByRecency(out)
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	return out
}

func sortByRecency(looms []Loom) {
	// Insertion sort: the list is capped at maxEntries and is almost always
	// already ordered, so this is the cheap and obvious one.
	for i := 1; i < len(looms); i++ {
		for j := i; j > 0 && looms[j].LastOpened.After(looms[j-1].LastOpened); j-- {
			looms[j], looms[j-1] = looms[j-1], looms[j]
		}
	}
}

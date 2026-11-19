package userconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// appDir is EasyLoom's own directory inside the XDG configuration home.
const appDir = "easyloom"

// Getenv is the environment lookup a resolver reads through. Every entry point
// takes one rather than reading os.Getenv, because the interesting cases here
// are an XDG_CONFIG_HOME that is set, unset, and relative, and a test cannot
// arrange all three against the real environment without leaking into its
// neighbours.
type Getenv func(string) string

// OS is the Getenv that reads the real environment.
var OS Getenv = os.Getenv

// Dir reports EasyLoom's configuration directory: $XDG_CONFIG_HOME/easyloom
// when that variable names an absolute path, otherwise ~/.config/easyloom.
//
// It does not create anything. A caller asking where the registry would be —
// the picker, deciding whether there is one — must not leave a directory
// behind on a machine that never saves a registry.
//
// A relative $XDG_CONFIG_HOME is ignored rather than joined to the working
// directory, which is what the XDG specification requires and is in any case
// the kinder reading: a configuration path that moves with cd is worse than
// the default.
func Dir(getenv Getenv) (string, error) {
	if getenv == nil {
		getenv = OS
	}
	if xdg := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, appDir), nil
	}
	home, err := homeDir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appDir), nil
}

// File reports the path a named file would have inside [Dir]. It creates
// nothing either; see [Ensure].
func File(getenv Getenv, name string) (string, error) {
	if name == "" {
		return "", errors.New("user config: no file name")
	}
	if name != filepath.Base(name) {
		return "", fmt.Errorf("user config: %q is not a file name", name)
	}
	dir, err := Dir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Ensure resolves the configuration directory and creates it if it is not
// there, returning the path. This is the call a writer makes; readers use
// [Dir] or [File] and treat a missing directory as no configuration.
func Ensure(getenv Getenv) (string, error) {
	dir, err := Dir(getenv)
	if err != nil {
		return "", err
	}
	// 0o700: nothing here is a secret today, but agent profiles expand to
	// command lines and the directory sits in a home directory. The permissive
	// version of that choice is not one to make by default.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("user config: %w", err)
	}
	return dir, nil
}

// homeDir resolves the home directory through getenv first so that a test can
// point it somewhere disposable, falling back to the toolchain's own lookup —
// which knows about platforms where $HOME is not the answer.
func homeDir(getenv Getenv) (string, error) {
	if home := getenv("HOME"); filepath.IsAbs(home) {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user config: home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("user config: home directory %q is not absolute", home)
	}
	return home, nil
}

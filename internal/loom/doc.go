// Package loom invokes the mutating half of the boundary.
//
// internal/snapshot is how EasyLoom reads a loom; this is how it writes to
// one. The protocol is explicit that a viewer "reads this projection and
// performs mutations only by invoking Loom commands", so nothing here — and
// nothing above it — edits .loom itself: not the queue file, not a directory
// suffix, not completed-at.
//
// Both frontends are thin skins over this package. The TUI keybindings and the
// future HTTP handlers build no argv of their own.
package loom

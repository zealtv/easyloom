// Package warm holds several looms open at once.
//
// Opening a loom costs a map run and a filesystem watcher, which is seconds on
// a large loom. A session that switches between looms pays that once per loom
// rather than once per switch: the set keeps each one's cache and watcher
// alive, and switching back is a redraw.
//
// It is bounded, because every warm loom is a watcher, a cache and a
// subprocess the shutdown path has to reach. Eviction is closing, and it is
// refusable: an agent working a loom's queue must not have its loom closed out
// from under it.
package warm

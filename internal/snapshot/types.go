package snapshot

// The types in this file mirror the `map --json` schema documented in
// .loom/docs/protocol-v2.md ("Map projection"). They are a transcription of
// that projection and nothing more: ready, frontier, queue_position and
// waiting_inherited are authoritative, and nothing here recomputes them.

// SupportedSchemaVersion is the newest map projection schema this model
// transcribes. Schema 2 added the inert dependency status and nothing else, so
// a schema 1 projection is a schema 2 projection that never says inert, and
// MinSchemaVersion lets looms that predate the bump keep working.
const SupportedSchemaVersion = 2

// MinSchemaVersion is the oldest map projection schema this model reads.
const MinSchemaVersion = 1

// SupportsSchema reports whether this model reads map schema v.
func SupportsSchema(v int) bool {
	return v >= MinSchemaVersion && v <= SupportedSchemaVersion
}

// FormatVersionV2 is the loom format the projection exists for at all. A v1
// loom has no map command, so it never produces a snapshot.
const FormatVersionV2 = 2

// Tray is the region of the loom a stitch lives in.
type Tray string

const (
	TrayThreads       Tray = "threads"
	TrayTied          Tray = "tied"
	TrayDropped       Tray = "dropped"
	TrayLegacyTied    Tray = "legacy-tied"
	TrayLegacyDropped Tray = "legacy-dropped"
)

// State is a stitch's lifecycle state.
type State string

const (
	StatePlain     State = "plain"
	StateStitching State = "stitching"
	StateWaiting   State = "waiting"
	StateTending   State = "tending"
	StateTied      State = "tied"
	StateDropped   State = "dropped"
	StateAbandoned State = "abandoned"
)

// DependencyStatus is the resolution of a single needs/ edge.
type DependencyStatus string

const (
	DependencySatisfied DependencyStatus = "satisfied"
	DependencyBlocked   DependencyStatus = "blocked"
	DependencyBroken    DependencyStatus = "broken"
	// DependencyInert is an unsatisfied edge whose dependent is tied, dropped,
	// abandoned or archived (schema 2). It can block nothing: it is history,
	// not health, and viewers show it faint rather than as a problem.
	DependencyInert DependencyStatus = "inert"
)

// BrokenReason explains a broken dependency. It is null for any other status.
type BrokenReason string

const (
	BrokenMissing   BrokenReason = "missing"
	BrokenDropped   BrokenReason = "dropped"
	BrokenAmbiguous BrokenReason = "ambiguous"
	BrokenInvalid   BrokenReason = "invalid"
)

// Severity is a diagnostic's severity.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Snapshot is one whole-loom map projection.
type Snapshot struct {
	SchemaVersion      int             `json:"schema_version"`
	FormatVersion      int             `json:"format_version"`
	LoomRoot           string          `json:"loom_root"`
	Stitches           []Stitch        `json:"stitches"`
	DecompositionEdges []Decomposition `json:"decomposition_edges"`
	DependencyEdges    []Dependency    `json:"dependency_edges"`
	Cycles             [][]string      `json:"cycles"`
	Frontier           []string        `json:"frontier"`
	RecentlyCompleted  []string        `json:"recently_completed"`
	Diagnostics        []Diagnostic    `json:"diagnostics"`
}

// Stitch is one stitch in the projection.
type Stitch struct {
	ID       string  `json:"id"`
	RootID   string  `json:"root_id"`
	ParentID *string `json:"parent_id"`
	// Path is slash-separated and relative to .loom.
	Path  string `json:"path"`
	Tray  Tray   `json:"tray"`
	State State  `json:"state"`
	// Ready is authoritative. Do not derive readiness from state and edges.
	Ready            bool `json:"ready"`
	WaitingInherited bool `json:"waiting_inherited"`
	// QueuePosition is one-based, or nil when the stitch is not queued.
	QueuePosition *int `json:"queue_position"`
	// CompletedAt is an ISO-8601 string, or nil. It is nil on legacy records,
	// and the protocol forbids guessing it from mtime: render it as unknown,
	// never as a fabricated date.
	CompletedAt  *string      `json:"completed_at"`
	Archived     bool         `json:"archived"`
	Legacy       bool         `json:"legacy"`
	Children     []string     `json:"children"`
	Dependencies []Dependency `json:"dependencies"`
	Cycle        []string     `json:"cycle"`
}

// Decomposition is a parent/child edge in the decomposition tree.
type Decomposition struct {
	Parent string `json:"parent"`
	Child  string `json:"child"`
}

// Dependency is one needs/ edge: From needs To.
type Dependency struct {
	From   string           `json:"from"`
	To     string           `json:"to"`
	Status DependencyStatus `json:"status"`
	// Reason is nil unless Status is broken.
	Reason *BrokenReason `json:"reason"`
}

// Diagnostic is one problem the loom reported while building the projection.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	StitchID *string  `json:"stitch_id"`
	TargetID *string  `json:"target_id"`
}

// Index maps stitch ID to the stitch. The returned pointers alias the
// snapshot's own slice, so the snapshot must be treated as immutable.
func (s *Snapshot) Index() map[string]*Stitch {
	index := make(map[string]*Stitch, len(s.Stitches))
	for i := range s.Stitches {
		index[s.Stitches[i].ID] = &s.Stitches[i]
	}
	return index
}

package snapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DriftError reports that the projection did not match the schema this model
// transcribes: an unknown field, an unknown enum value, or an unexpected
// schema version. It means the schema moved, not that the data is unusable.
//
// Decode returns a decoded snapshot alongside a DriftError whenever it could
// still read the document, so a caller may log the drift and carry on.
type DriftError struct {
	Issues []string
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("map projection schema drift: %s", strings.Join(e.Issues, "; "))
}

// Decode parses a `map --json` document.
//
// Decoding is strict: an unknown field is a signal that the schema moved, and
// is surfaced as a *DriftError. Unknown enum values and an unexpected
// schema_version are surfaced the same way. In each of those cases the parsed
// snapshot is returned as well, so the caller chooses between degrading and
// failing. A nil snapshot means the document could not be parsed at all.
func Decode(data []byte) (*Snapshot, error) {
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()

	var snap Snapshot
	if err := strict.Decode(&snap); err != nil {
		field, ok := unknownField(err)
		if !ok {
			return nil, fmt.Errorf("parse map projection: %w", err)
		}
		// Unknown fields abort the strict decoder partway through, so reparse
		// leniently to get a usable snapshot to hand back with the drift.
		var lenient Snapshot
		if err := json.Unmarshal(data, &lenient); err != nil {
			return nil, fmt.Errorf("parse map projection: %w", err)
		}
		issues := append([]string{fmt.Sprintf("unknown field %q", field)}, validate(&lenient)...)
		return &lenient, &DriftError{Issues: issues}
	}

	if issues := validate(&snap); len(issues) > 0 {
		return &snap, &DriftError{Issues: issues}
	}
	return &snap, nil
}

// unknownField extracts the field name from the standard library's unknown
// field error, which carries no type of its own.
func unknownField(err error) (string, bool) {
	const prefix = "json: unknown field "
	msg := err.Error()
	i := strings.Index(msg, prefix)
	if i < 0 {
		return "", false
	}
	return strings.Trim(msg[i+len(prefix):], `"`), true
}

func validate(s *Snapshot) []string {
	seen := map[string]bool{}
	var issues []string
	note := func(format string, args ...any) {
		issue := fmt.Sprintf(format, args...)
		if !seen[issue] {
			seen[issue] = true
			issues = append(issues, issue)
		}
	}

	if !SupportsSchema(s.SchemaVersion) {
		note("schema_version %d, supported %d–%d", s.SchemaVersion, MinSchemaVersion, SupportedSchemaVersion)
	}
	if s.FormatVersion != FormatVersionV2 {
		note("format_version %d, expected %d", s.FormatVersion, FormatVersionV2)
	}

	for _, st := range s.Stitches {
		switch st.Tray {
		case TrayThreads, TrayTied, TrayDropped, TrayLegacyTied, TrayLegacyDropped:
		default:
			note("unknown tray %q", st.Tray)
		}
		switch st.State {
		case StatePlain, StateStitching, StateWaiting, StateTending, StateTied, StateDropped, StateAbandoned:
		default:
			note("unknown state %q", st.State)
		}
		for _, dep := range st.Dependencies {
			validateDependency(dep, note)
		}
	}
	for _, dep := range s.DependencyEdges {
		validateDependency(dep, note)
	}
	for _, d := range s.Diagnostics {
		switch d.Severity {
		case SeverityWarning, SeverityError:
		default:
			note("unknown diagnostic severity %q", d.Severity)
		}
	}

	sort.Strings(issues)
	return issues
}

func validateDependency(dep Dependency, note func(string, ...any)) {
	switch dep.Status {
	case DependencySatisfied, DependencyBlocked, DependencyBroken, DependencyInert:
	default:
		note("unknown dependency status %q", dep.Status)
	}
	if dep.Reason == nil {
		return
	}
	switch *dep.Reason {
	case BrokenMissing, BrokenDropped, BrokenAmbiguous, BrokenInvalid:
	default:
		note("unknown broken reason %q", *dep.Reason)
	}
}

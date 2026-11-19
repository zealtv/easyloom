package snapshot

import (
	"errors"
	"strings"
	"testing"
)

// A minimal hand-written projection. Golden JSON captured from another repo's
// loom is deliberately not used: the schema is versioned but the data is not
// ours.
const validProjection = `{
  "schema_version": 1,
  "format_version": 2,
  "loom_root": "/repo/.loom",
  "stitches": [
    {
      "id": "goal",
      "root_id": "goal",
      "parent_id": null,
      "path": "threads/goal",
      "tray": "threads",
      "state": "plain",
      "ready": false,
      "waiting_inherited": false,
      "queue_position": null,
      "completed_at": null,
      "archived": false,
      "legacy": false,
      "children": ["first", "second"],
      "dependencies": [],
      "cycle": []
    },
    {
      "id": "second",
      "root_id": "goal",
      "parent_id": "goal",
      "path": "threads/goal/second",
      "tray": "threads",
      "state": "plain",
      "ready": false,
      "waiting_inherited": false,
      "queue_position": 1,
      "completed_at": null,
      "archived": false,
      "legacy": false,
      "children": [],
      "dependencies": [
        {"from": "second", "to": "first", "status": "blocked", "reason": null}
      ],
      "cycle": []
    }
  ],
  "decomposition_edges": [
    {"parent": "goal", "child": "first"},
    {"parent": "goal", "child": "second"}
  ],
  "dependency_edges": [
    {"from": "second", "to": "first", "status": "blocked", "reason": null}
  ],
  "cycles": [],
  "frontier": ["first"],
  "recently_completed": [],
  "diagnostics": [
    {"severity": "warning", "code": "queue-stale", "message": "stale record",
     "stitch_id": "second", "target_id": null}
  ]
}`

func TestDecodeValid(t *testing.T) {
	snap, err := Decode([]byte(validProjection))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if snap.SchemaVersion != 1 || snap.FormatVersion != 2 {
		t.Errorf("versions = %d/%d, want 1/2", snap.SchemaVersion, snap.FormatVersion)
	}
	if snap.LoomRoot != "/repo/.loom" {
		t.Errorf("LoomRoot = %q", snap.LoomRoot)
	}
	if got := len(snap.Stitches); got != 2 {
		t.Fatalf("len(Stitches) = %d, want 2", got)
	}

	goal := snap.Stitches[0]
	if goal.ParentID != nil {
		t.Errorf("goal.ParentID = %v, want nil", *goal.ParentID)
	}
	if goal.QueuePosition != nil {
		t.Errorf("goal.QueuePosition = %v, want nil", *goal.QueuePosition)
	}
	if goal.CompletedAt != nil {
		t.Errorf("goal.CompletedAt = %v, want nil", *goal.CompletedAt)
	}

	second := snap.Stitches[1]
	if second.ParentID == nil || *second.ParentID != "goal" {
		t.Errorf("second.ParentID = %v, want goal", second.ParentID)
	}
	if second.QueuePosition == nil || *second.QueuePosition != 1 {
		t.Errorf("second.QueuePosition = %v, want 1", second.QueuePosition)
	}
	if len(second.Dependencies) != 1 || second.Dependencies[0].Status != DependencyBlocked {
		t.Errorf("second.Dependencies = %+v", second.Dependencies)
	}
	if second.Dependencies[0].Reason != nil {
		t.Errorf("blocked dependency carries a reason: %v", *second.Dependencies[0].Reason)
	}

	if len(snap.Frontier) != 1 || snap.Frontier[0] != "first" {
		t.Errorf("Frontier = %v", snap.Frontier)
	}
	if len(snap.Diagnostics) != 1 || snap.Diagnostics[0].Severity != SeverityWarning {
		t.Errorf("Diagnostics = %+v", snap.Diagnostics)
	}
	if snap.Diagnostics[0].TargetID != nil {
		t.Errorf("TargetID = %v, want nil", *snap.Diagnostics[0].TargetID)
	}
}

func TestDecodeBrokenDependencyReason(t *testing.T) {
	doc := strings.ReplaceAll(validProjection,
		`{"from": "second", "to": "first", "status": "blocked", "reason": null}`,
		`{"from": "second", "to": "first", "status": "broken", "reason": "missing"}`)

	snap, err := Decode([]byte(doc))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	dep := snap.Stitches[1].Dependencies[0]
	if dep.Status != DependencyBroken {
		t.Fatalf("Status = %q, want broken", dep.Status)
	}
	if dep.Reason == nil || *dep.Reason != BrokenMissing {
		t.Errorf("Reason = %v, want missing", dep.Reason)
	}
}

// Schema 2 added the inert status for edges whose dependent can no longer be
// blocked. It decodes without drift, and schema 1 still does too.
func TestDecodeSchema2InertDependency(t *testing.T) {
	doc := strings.Replace(validProjection, `"schema_version": 1`, `"schema_version": 2`, 1)
	doc = strings.ReplaceAll(doc,
		`{"from": "second", "to": "first", "status": "blocked", "reason": null}`,
		`{"from": "second", "to": "first", "status": "inert", "reason": null}`)

	snap, err := Decode([]byte(doc))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if snap.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", snap.SchemaVersion)
	}
	if dep := snap.Stitches[1].Dependencies[0]; dep.Status != DependencyInert {
		t.Errorf("Status = %q, want inert", dep.Status)
	}
}

func TestDecodeUnknownFieldIsDriftNotFailure(t *testing.T) {
	doc := strings.Replace(validProjection,
		`"frontier": ["first"],`,
		`"frontier": ["first"], "tension": 3,`, 1)

	snap, err := Decode([]byte(doc))
	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("err = %v, want *DriftError", err)
	}
	if snap == nil {
		t.Fatal("Decode returned no snapshot alongside the drift")
	}
	if len(snap.Stitches) != 2 {
		t.Errorf("snapshot not usable: %d stitches", len(snap.Stitches))
	}
	if !strings.Contains(drift.Error(), `unknown field "tension"`) {
		t.Errorf("drift = %q", drift.Error())
	}
}

func TestDecodeUnknownEnumValuesAreDrift(t *testing.T) {
	doc := strings.Replace(validProjection, `"state": "plain"`, `"state": "unravelled"`, 1)

	snap, err := Decode([]byte(doc))
	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("err = %v, want *DriftError", err)
	}
	if snap == nil {
		t.Fatal("Decode returned no snapshot alongside the drift")
	}
	if !strings.Contains(drift.Error(), `unknown state "unravelled"`) {
		t.Errorf("drift = %q", drift.Error())
	}
}

func TestDecodeUnexpectedSchemaVersionIsDrift(t *testing.T) {
	doc := strings.Replace(validProjection, `"schema_version": 1`, `"schema_version": 3`, 1)

	if _, err := Decode([]byte(doc)); err == nil {
		t.Fatal("Decode accepted schema_version 3 silently")
	}
}

func TestDecodeMalformedFails(t *testing.T) {
	snap, err := Decode([]byte(`{"schema_version": `))
	if err == nil {
		t.Fatal("Decode accepted a truncated document")
	}
	if snap != nil {
		t.Error("Decode returned a snapshot for an unparseable document")
	}
}

func TestIndex(t *testing.T) {
	snap, err := Decode([]byte(validProjection))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	index := snap.Index()
	if len(index) != 2 {
		t.Fatalf("len(index) = %d, want 2", len(index))
	}
	if index["second"] != &snap.Stitches[1] {
		t.Error("index does not alias the snapshot's own stitches")
	}
}

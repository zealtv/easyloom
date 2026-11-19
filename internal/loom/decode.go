package loom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// mutationResult mirrors the structured mutation result documented in
// .loom/docs/protocol-v2.md ("Structured mutation results"). It is a
// transcription of that object and nothing more.
type mutationResult struct {
	SchemaVersion int     `json:"schema_version"`
	FormatVersion int     `json:"format_version"`
	Command       string  `json:"command"`
	OK            bool    `json:"ok"`
	Changed       bool    `json:"changed"`
	ID            *string `json:"id"`
	State         *string `json:"state"`
	Path          *string `json:"path"`
	Tray          *string `json:"tray"`
	QueuePosition *int    `json:"queue_position"`
	CompletedAt   *string `json:"completed_at"`
	Error         *struct {
		Code      string   `json:"code"`
		Message   string   `json:"message"`
		StitchIDs []string `json:"stitch_ids"`
	} `json:"error"`
}

// decodeResult parses one mutation result.
//
// A nil result means the output was not a mutation result at all — prose from
// a script that predates --json, most likely — and never that the mutation
// failed. A failure is a well-formed result with ok false, and comes back with
// ok false and a usable *Result.
//
// Decoding is strict for the same reason the projection's is: an unknown field
// means the schema moved. That is reported as drift alongside a usable result,
// because a viewer that stops working on a loom that gained a field is worse
// than one that reads the fields it knows.
func decodeResult(stdout string) (res *Result, ok bool, drift *DriftError) {
	data := []byte(stdout)
	// A successful mutation with --json prints exactly one JSON object and
	// nothing else, so anything that does not start like one is prose.
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, false, nil
	}

	var issues []string
	var decoded mutationResult

	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&decoded); err != nil {
		field, unknown := unknownField(err)
		if !unknown {
			return nil, false, nil
		}
		// The strict decoder abandons the document at the unknown field, so
		// reparse leniently to get something usable to hand back with it.
		decoded = mutationResult{}
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, false, nil
		}
		issues = append(issues, fmt.Sprintf("unknown field %q", field))
	}

	issues = append(issues, validate(&decoded)...)

	res = &Result{
		SchemaVersion: decoded.SchemaVersion,
		FormatVersion: decoded.FormatVersion,
		Command:       Command(decoded.Command),
		Changed:       decoded.Changed,
		ID:            deref(decoded.ID),
		State:         snapshot.State(deref(decoded.State)),
		Path:          deref(decoded.Path),
		Tray:          snapshot.Tray(deref(decoded.Tray)),
		QueuePosition: decoded.QueuePosition,
		CompletedAt:   deref(decoded.CompletedAt),
	}
	if decoded.Error != nil {
		res.errCode = ErrorCode(decoded.Error.Code)
		res.errMessage = decoded.Error.Message
		res.errStitchIDs = decoded.Error.StitchIDs
	}
	if len(issues) > 0 {
		sort.Strings(issues)
		drift = &DriftError{Issues: issues}
	}
	return res, decoded.OK, drift
}

// validate reports what moved. The error code set is explicitly open, so an
// unrecognised code is never drift; the state and tray vocabularies are the
// projection's, and are.
func validate(r *mutationResult) []string {
	var issues []string
	if r.SchemaVersion != SupportedSchemaVersion {
		issues = append(issues, fmt.Sprintf("schema_version %d, supported %d",
			r.SchemaVersion, SupportedSchemaVersion))
	}
	if r.FormatVersion != snapshot.FormatVersionV2 {
		issues = append(issues, fmt.Sprintf("format_version %d, expected %d",
			r.FormatVersion, snapshot.FormatVersionV2))
	}
	if r.State != nil {
		switch snapshot.State(*r.State) {
		case snapshot.StatePlain, snapshot.StateStitching, snapshot.StateWaiting,
			snapshot.StateTending, snapshot.StateTied, snapshot.StateDropped,
			snapshot.StateAbandoned:
		default:
			issues = append(issues, fmt.Sprintf("unknown state %q", *r.State))
		}
	}
	if r.Tray != nil {
		switch snapshot.Tray(*r.Tray) {
		case snapshot.TrayThreads, snapshot.TrayTied, snapshot.TrayDropped,
			snapshot.TrayLegacyTied, snapshot.TrayLegacyDropped:
		default:
			issues = append(issues, fmt.Sprintf("unknown tray %q", *r.Tray))
		}
	}
	return issues
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

// deref flattens a nullable string. The protocol's nulls all mean "there is no
// such thing here", which an empty string says just as well and every caller
// finds easier to render.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

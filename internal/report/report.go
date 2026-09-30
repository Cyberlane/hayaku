// Package report renders plans and validates untrusted plan documents.
package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxPlan = 64 << 20

// Equal compares the versioned wire contract, avoiding nil/empty differences in
// optional fields introduced by JSON decoding. Unknown fields still reject.
func Equal(a, b model.Plan) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

// Differences names changed fields without exposing source or environment data.
func Differences(a, b model.Plan) []string {
	left, right := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	l, _ := json.Marshal(a)
	r, _ := json.Marshal(b)
	_ = json.Unmarshal(l, &left)
	_ = json.Unmarshal(r, &right)
	var changed []string
	for key, value := range left {
		if !bytes.Equal(value, right[key]) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func Decode(r io.Reader) (model.Plan, error) {
	var p model.Plan
	b, err := io.ReadAll(io.LimitReader(r, MaxPlan+1))
	if err != nil {
		return p, err
	}
	if len(b) > MaxPlan {
		return p, errors.New("oversized plan JSON")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, errors.New("trailing or oversized plan JSON")
	}
	if p.Schema != model.Schema {
		return p, fmt.Errorf("unsupported plan schema %d", p.Schema)
	}
	if p.Mode != "full-fallback" {
		return p, errors.New("unqualified execution mode")
	}
	if p.Base == "" || p.Candidate == "" || p.ConfigDigest == "" || p.ContextDigest == "" || p.ToolsDigest == "" || len(p.Commands) == 0 {
		return p, errors.New("incomplete plan identity or commands")
	}
	return p, nil
}

func JSON(w io.Writer, v any) error {
	d := json.NewEncoder(w)
	d.SetIndent("", "  ")
	return d.Encode(v)
}

func Human(w io.Writer, p model.Plan) error {
	if _, err := fmt.Fprintf(w, "Hayaku %s — %s\nBase: %s\nCandidate: %s\nChanges: %d; graph proposal: %d units; required: %d units\n", p.Version, p.Mode, p.Base, p.Candidate, len(p.Changes), len(p.Proposed), len(p.Selected)); err != nil {
		return err
	}
	for _, gap := range p.Gaps {
		if _, err := fmt.Fprintf(w, "Gap [%s] %s: %s\n", gap.Workspace, gap.Code, gap.Detail); err != nil {
			return err
		}
	}
	for _, reason := range p.Reasons {
		if _, err := fmt.Fprintf(w, "Unit %q reason=%s input=%q via=%q\n", reason.Unit, reason.Code, reason.Input, reason.Via); err != nil {
			return err
		}
	}
	for _, unit := range p.Proposed {
		if _, err := fmt.Fprintf(w, "Shadow candidate %q (%s)\n", unit.ID, unit.Selector); err != nil {
			return err
		}
	}
	for _, cmd := range p.Commands {
		if _, err := fmt.Fprintf(w, "Required cwd=%q executable=%q argv=%q\n", cmd.Dir, cmd.Executable, cmd.Args); err != nil {
			return err
		}
	}
	return nil
}

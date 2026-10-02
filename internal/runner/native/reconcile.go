// Package native reconciles bounded private runner metadata. Native discovery
// and successful reconciliation do not establish runtime omission authority.
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxResultBytes = 16 << 20

type Outcome struct {
	Unit   string `json:"unit"`
	Test   string `json:"test,omitempty"`
	Action string `json:"action"`
}
type Result struct {
	Units           []Outcome `json:"units"`
	Tests           []Outcome `json:"tests"`
	Missing         []string  `json:"missing"`
	Failed          bool      `json:"failed"`
	UnhandledErrors int       `json:"unhandled_errors,omitempty"`
}
type Terminal struct {
	Selector string `json:"selector"`
	Test     string `json:"test,omitempty"`
	Action   string `json:"action"`
}
type Report struct {
	Schema   int        `json:"schema"`
	Runner   string     `json:"runner"`
	Version  string     `json:"version"`
	Complete bool       `json:"complete"`
	Errors   int        `json:"errors"`
	Units    []Terminal `json:"units"`
	Tests    []Terminal `json:"tests"`
}

// DecodeJSON rejects duplicate object fields as well as unknown fields and
// trailing data. Limits apply before decoding, including ignored diagnostics.
func DecodeJSON(data []byte, dest any) error {
	if len(data) > MaxResultBytes {
		return errors.New("native metadata exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("native metadata nesting exceeds limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			keys := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate native metadata field")
				}
				keys[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("invalid native metadata JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing native metadata")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dest); err != nil {
		return errors.New("invalid native metadata fields")
	}
	return nil
}

// Reconcile requires exactly one terminal outcome per expected native unit.
// The caller independently verifies successful native process completion.
func Reconcile(r io.Reader, runner, version string, expected []model.Unit) (Result, error) {
	result := Result{Units: []Outcome{}, Tests: []Outcome{}, Missing: []string{}}
	if runner == "" || version == "" || len(expected) == 0 || len(expected) > 50000 {
		return result, errors.New("invalid native expected inventory")
	}
	want := map[string]string{}
	ids := map[string]bool{}
	for _, u := range expected {
		if u.ID == "" || u.Workspace == "" || u.Kind != runner+"-file" || !safe(u.Selector) || want[u.Selector] != "" || ids[u.ID] {
			return result, errors.New("invalid native expected unit")
		}
		want[u.Selector] = u.ID
		ids[u.ID] = true
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxResultBytes+1))
	if err != nil {
		return result, errors.New("native report read failed")
	}
	var report Report
	if err = DecodeJSON(data, &report); err != nil {
		return result, err
	}
	if report.Schema != 1 || report.Runner != runner || report.Version != version || !report.Complete || report.Errors < 0 || len(report.Units) > 50000 || len(report.Tests) > 100000 {
		return result, errors.New("incomplete or incompatible native report")
	}
	result.Failed = report.Errors > 0
	result.UnhandledErrors = report.Errors
	seen, cases := map[string]bool{}, map[string]bool{}
	for _, item := range report.Units {
		if want[item.Selector] == "" || item.Test != "" || seen[item.Selector] || !terminal(item.Action) {
			return result, errors.New("unexpected or duplicate native unit terminal")
		}
		seen[item.Selector] = true
		result.Units = append(result.Units, Outcome{Unit: want[item.Selector], Action: item.Action})
		result.Failed = result.Failed || item.Action == "fail"
	}
	for _, item := range report.Tests {
		key := item.Selector + "\x00" + item.Test
		if want[item.Selector] == "" || !safe(item.Test) || cases[key] || !terminal(item.Action) {
			return result, errors.New("unexpected or duplicate native test terminal")
		}
		cases[key] = true
		result.Tests = append(result.Tests, Outcome{Unit: want[item.Selector], Test: item.Test, Action: item.Action})
		result.Failed = result.Failed || item.Action == "fail"
	}
	for selector, id := range want {
		if !seen[selector] {
			result.Missing = append(result.Missing, id)
		}
	}
	sortResult(&result)
	if len(result.Missing) != 0 {
		return result, errors.New("missing native unit terminal outcomes")
	}
	if result.UnhandledErrors > 0 {
		return result, errors.New("unhandled native errors invalidate complete reconciliation")
	}
	return result, nil
}
func safe(s string) bool     { return s != "" && len(s) <= 8192 && !strings.ContainsAny(s, "\x00\r\n") }
func terminal(s string) bool { return s == "pass" || s == "fail" || s == "skip" }
func sortResult(r *Result) {
	sort.Strings(r.Missing)
	sort.Slice(r.Units, func(i, j int) bool { return r.Units[i].Unit < r.Units[j].Unit })
	sort.Slice(r.Tests, func(i, j int) bool {
		if r.Tests[i].Unit != r.Tests[j].Unit {
			return r.Tests[i].Unit < r.Tests[j].Unit
		}
		return r.Tests[i].Test < r.Tests[j].Test
	})
}

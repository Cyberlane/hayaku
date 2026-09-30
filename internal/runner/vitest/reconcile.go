// Package vitest validates the bounded private native reporter protocol.
package vitest

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

const maxResultBytes = 32 << 20

type Outcome struct {
	Unit   string `json:"unit"`
	Test   string `json:"test,omitempty"`
	Action string `json:"action"`
}
type Result struct {
	Files           []Outcome `json:"files"`
	Tests           []Outcome `json:"tests"`
	Missing         []string  `json:"missing"`
	Failed          bool      `json:"failed"`
	UnhandledErrors int       `json:"unhandled_errors"`
}
type nativeOutcome struct {
	File    string `json:"file"`
	Project string `json:"project"`
	Test    string `json:"test,omitempty"`
	Action  string `json:"action"`
}
type nativeResult struct {
	Schema   int             `json:"schema"`
	Complete bool            `json:"complete"`
	Errors   int             `json:"errors"`
	Files    []nativeOutcome `json:"files"`
	Tests    []nativeOutcome `json:"tests"`
}

// Reconcile requires exactly one terminal file outcome for each configured
// expected unit. Native errors and failures remain failures; output is not saved.
// A successful native process exit must independently be required by the caller.
func Reconcile(r io.Reader, expected []model.Unit) (Result, error) {
	result := Result{}
	if len(expected) == 0 {
		return result, errors.New("Vitest reconciliation requires expected units")
	}
	want := map[string]model.Unit{}
	keys := map[string]string{}
	for _, u := range expected {
		prefix := u.Workspace + ":"
		rest, ok := strings.CutPrefix(u.ID, prefix)
		if !ok || u.Kind != "vitest-file" || u.Workspace == "" || want[u.ID].ID != "" {
			return result, errors.New("invalid Vitest expected inventory")
		}
		project, file, ok := strings.Cut(rest, ":")
		if !ok || file == "" || strings.Contains(file, "\\") || strings.HasPrefix(file, "/") || file == ".." || strings.HasPrefix(file, "../") {
			return result, errors.New("invalid Vitest expected identity")
		}
		key := project + "\x00" + file
		if keys[key] != "" {
			return result, errors.New("duplicate Vitest expected scope")
		}
		keys[key] = u.ID
		want[u.ID] = u
	}
	data, err := io.ReadAll(io.LimitReader(r, maxResultBytes+1))
	if err != nil || len(data) > maxResultBytes {
		return result, errors.New("Vitest result output limit or read failure")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var report nativeResult
	if err = decoder.Decode(&report); err != nil {
		return result, errors.New("invalid Vitest native result JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return result, errors.New("trailing Vitest native result data")
	}
	if report.Schema != 1 || !report.Complete || report.Errors < 0 {
		return result, errors.New("incomplete Vitest native result")
	}
	result.Failed = report.Errors > 0
	result.UnhandledErrors = report.Errors
	files := map[string]bool{}
	tests := map[string]bool{}
	add := func(item nativeOutcome, isTest bool) error {
		unit := keys[item.Project+"\x00"+item.File]
		if unit == "" {
			return errors.New("unexpected Vitest outcome scope")
		}
		action := ""
		switch item.Action {
		case "passed":
			action = "pass"
		case "failed":
			action = "fail"
		case "skipped":
			action = "skip"
		default:
			return errors.New("non-terminal Vitest outcome")
		}
		outcome := Outcome{Unit: unit, Test: item.Test, Action: action}
		if isTest {
			if item.Test == "" || tests[unit+"\x00"+item.Test] {
				return errors.New("missing or duplicate Vitest test identity")
			}
			var identity []json.RawMessage
			if json.Unmarshal([]byte(item.Test), &identity) != nil || len(identity) != 2 {
				return errors.New("invalid Vitest test identity")
			}
			var name string
			var occurrence int
			if json.Unmarshal(identity[0], &name) != nil || json.Unmarshal(identity[1], &occurrence) != nil || occurrence < 0 {
				return errors.New("invalid Vitest test occurrence")
			}
			tests[unit+"\x00"+item.Test] = true
			result.Tests = append(result.Tests, outcome)
		} else {
			if item.Test != "" || files[unit] {
				return errors.New("duplicate or invalid Vitest file terminal")
			}
			files[unit] = true
			result.Files = append(result.Files, outcome)
		}
		if action == "fail" {
			result.Failed = true
		}
		return nil
	}
	for _, item := range report.Files {
		if err = add(item, false); err != nil {
			return result, err
		}
	}
	for _, item := range report.Tests {
		if err = add(item, true); err != nil {
			return result, err
		}
	}
	for id := range want {
		if !files[id] {
			result.Missing = append(result.Missing, id)
		}
	}
	sort.Strings(result.Missing)
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Unit < result.Files[j].Unit })
	sort.Slice(result.Tests, func(i, j int) bool {
		a, b := result.Tests[i], result.Tests[j]
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		return a.Test < b.Test
	})
	if len(result.Missing) > 0 {
		return result, errors.New("missing Vitest file terminal outcomes")
	}
	return result, nil
}

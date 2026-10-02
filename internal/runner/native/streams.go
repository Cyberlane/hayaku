package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func jsonLines(r io.Reader, visit func([]byte) error) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxResultBytes+1))
	if err != nil || len(data) > MaxResultBytes {
		return errors.New("native event stream read or size limit")
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return errors.New("native event stream is empty or truncated")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) > 200000 {
		return errors.New("native event stream record limit")
	}
	for _, line := range lines {
		if len(line) == 0 || len(line) > 1<<20 {
			return errors.New("invalid native event record size")
		}
		if err := visit(line); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileSwift validates Swift Testing ABI 0 and 6.3 JSONL. Case IDs are
// native function IDs, not parameter values; cancellation/repetition fails closed.
func ReconcileSwift(r io.Reader, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	declared, suites, started, failures := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	runStarted, runEnded := false, false
	abi := ""
	err = jsonLines(r, func(line []byte) error {
		var record struct {
			Version json.RawMessage `json:"version"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := DecodeJSON(line, &record); err != nil {
			return err
		}
		version := string(record.Version)
		if version != "0" && version != `"6.3"` {
			return errors.New("unsupported Swift Testing ABI")
		}
		if abi != "" && abi != version {
			return errors.New("mixed Swift Testing ABI versions")
		}
		abi = version
		if runEnded {
			return errors.New("Swift events after run terminal")
		}
		var payload map[string]json.RawMessage
		if err := DecodeJSON(record.Payload, &payload); err != nil {
			return err
		}
		get := func(key string) string { var value string; json.Unmarshal(payload[key], &value); return value }
		switch record.Kind {
		case "test":
			id, kind := get("id"), get("kind")
			if !safe(id) || declared[id] || suites[id] {
				return errors.New("invalid or duplicate Swift test declaration")
			}
			if kind == "suite" {
				suites[id] = true
				return nil
			}
			if kind != "function" || a.want[id] == "" {
				return errors.New("unexpected Swift function declaration")
			}
			declared[id] = true
		case "event":
			kind, id := get("kind"), get("testID")
			switch kind {
			case "runStarted":
				if runStarted {
					return errors.New("duplicate Swift run start")
				}
				runStarted = true
			case "runEnded":
				if !runStarted {
					return errors.New("Swift run ended before start")
				}
				runEnded = true
			case "testStarted":
				if !runStarted || !declared[id] && !suites[id] {
					return errors.New("unexpected Swift test start")
				}
				if suites[id] {
					return nil
				}
				if started[id] || a.seen[id] {
					return errors.New("duplicate Swift test start")
				}
				started[id] = true
			case "testEnded":
				if suites[id] {
					return nil
				}
				if !started[id] || !declared[id] {
					return errors.New("Swift test ended before start")
				}
				action := "pass"
				if failures[id] {
					action = "fail"
				}
				return a.add(id, action)
			case "testSkipped":
				if !runStarted || !declared[id] {
					return errors.New("unexpected Swift test skip")
				}
				return a.add(id, "skip")
			case "issueRecorded":
				if !runStarted {
					return errors.New("Swift issue before run start")
				}
				var issue struct {
					IsKnown        *bool           `json:"isKnown"`
					IsFailure      *bool           `json:"isFailure"`
					Severity       string          `json:"severity"`
					SourceLocation json.RawMessage `json:"sourceLocation"`
				}
				if err := DecodeJSON(payload["issue"], &issue); err != nil || issue.IsKnown == nil {
					return errors.New("invalid Swift issue")
				}
				isFailure := issue.IsFailure == nil || *issue.IsFailure
				if !*issue.IsKnown && isFailure {
					a.result.Failed = true
					if declared[id] {
						failures[id] = true
					} else if id != "" && !suites[id] {
						return errors.New("unexpected Swift issue owner")
					} else {
						a.result.UnhandledErrors++
					}
				}
			case "testCaseStarted", "testCaseEnded":
				if !runStarted || !started[id] || a.seen[id] {
					return errors.New("unexpected Swift parameterized case event")
				}
			case "valueAttached":
				if !runStarted {
					return errors.New("Swift attachment before run")
				}
			case "testCancelled", "testCaseCancelled":
				return errors.New("cancelled Swift test inventory")
			default:
				return errors.New("unsupported Swift event kind")
			}
		default:
			return errors.New("unsupported Swift record kind")
		}
		return nil
	})
	if err != nil {
		return a.result, err
	}
	if !runStarted || !runEnded {
		return a.result, errors.New("missing Swift run terminal")
	}
	for id := range a.want {
		if !declared[id] {
			return a.result, errors.New("missing Swift native function declaration")
		}
	}
	if a.result.UnhandledErrors > 0 {
		return a.result, errors.New("unhandled Swift issues invalidate reconciliation")
	}
	return a.finish()
}

// ReconcileLibtest validates a single Rust libtest/nextest format-0.1 stream.
// Multi-binary reports must be reconciled separately with distinct inventories.
// JSON support is experimental upstream and never enables omission authority.
func ReconcileLibtest(r io.Reader, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	started, ended := false, false
	active := map[string]bool{}
	count := 0
	passed, failed, skipped := 0, 0, 0
	err = jsonLines(r, func(line []byte) error {
		var e struct {
			Type        string          `json:"type"`
			Event       string          `json:"event"`
			Name        string          `json:"name"`
			TestCount   *int            `json:"test_count"`
			Passed      *int            `json:"passed"`
			Failed      *int            `json:"failed"`
			Ignored     *int            `json:"ignored"`
			Measured    *int            `json:"measured"`
			FilteredOut *int            `json:"filtered_out"`
			ExecTime    json.RawMessage `json:"exec_time"`
			Stdout      json.RawMessage `json:"stdout"`
			Reason      json.RawMessage `json:"reason"`
			Nextest     json.RawMessage `json:"nextest"`
		}
		if err := DecodeJSON(line, &e); err != nil {
			return err
		}
		if ended {
			return errors.New("Rust event after suite terminal")
		}
		if len(e.Nextest) > 0 {
			var extra struct {
				Crate      string `json:"crate"`
				TestBinary string `json:"test_binary"`
				Kind       string `json:"kind"`
			}
			if err := DecodeJSON(e.Nextest, &extra); err != nil || extra.Crate == "" || extra.TestBinary == "" {
				return errors.New("unsupported nextest JSON-plus metadata")
			}
		}
		switch e.Type {
		case "suite":
			if e.Event == "started" {
				if started || e.TestCount == nil || *e.TestCount != len(expected) {
					return errors.New("inconsistent Rust suite inventory")
				}
				started = true
				count = *e.TestCount
				return nil
			}
			if !started || (e.Event != "ok" && e.Event != "failed") || e.Passed == nil || e.Failed == nil || e.Ignored == nil || e.Measured == nil || e.FilteredOut == nil || *e.Measured != 0 || *e.FilteredOut < 0 || *e.Passed != passed || *e.Failed != failed || *e.Ignored != skipped || passed+failed+skipped != count {
				return errors.New("inconsistent or incomplete Rust suite terminal")
			}
			if e.Event == "ok" && failed > 0 {
				return errors.New("conflicting Rust suite status")
			}
			if e.Event == "failed" {
				if failed == 0 {
					return errors.New("unhandled Rust suite failure")
				}
				a.result.Failed = true
			}
			ended = true
		case "test":
			if !started || a.want[e.Name] == "" {
				return errors.New("unexpected Rust native test")
			}
			if e.Event == "started" {
				if active[e.Name] || a.seen[e.Name] {
					return errors.New("duplicate Rust test start")
				}
				active[e.Name] = true
				return nil
			}
			action := ""
			switch e.Event {
			case "ok":
				if !active[e.Name] {
					return errors.New("Rust test passed before start")
				}
				action = "pass"
				passed++
			case "failed", "timeout":
				if !active[e.Name] {
					return errors.New("Rust test failed before start")
				}
				action = "fail"
				failed++
			case "ignored":
				action = "skip"
				skipped++
			default:
				return errors.New("unsupported Rust test event")
			}
			return a.add(e.Name, action)
		default:
			return errors.New("unsupported Rust event type")
		}
		return nil
	})
	if err != nil {
		return a.result, err
	}
	if !ended {
		return a.result, errors.New("missing Rust suite terminal")
	}
	return a.finish()
}

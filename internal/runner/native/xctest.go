package native

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

// ReconcileXCTest supports the serial Darwin XCTest console protocol. Case
// ownership comes from an independent list; process completion is separate.
func ReconcileXCTest(r io.Reader, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	names := map[string]string{}
	for _, c := range expected {
		var pair []string
		if json.Unmarshal([]byte(c.ID), &pair) != nil || len(pair) != 2 {
			return a.result, errors.New("invalid XCTest expected identity")
		}
		names["-["+pair[0]+" "+pair[1]+"]"] = c.ID
	}
	pattern := regexp.MustCompile(`^Test Case '(.+)' (started\.|passed \(|failed \(|skipped \()`)
	started := map[string]bool{}
	ended := false
	runOpen := false
	runFailures := 0
	s := bufio.NewScanner(io.LimitReader(r, MaxResultBytes+1))
	s.Buffer(make([]byte, 4096), MaxResultBytes+1)
	total := 0
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "Test Suite 'All tests' started at ") || strings.HasPrefix(line, "Test Suite 'Selected tests' started at ") {
			if runOpen {
				return a.result, errors.New("overlapping XCTest runs")
			}
			runOpen = true
			ended = false
			runFailures = 0
		}
		total += len(line) + 1
		if total > MaxResultBytes {
			return a.result, errors.New("XCTest output exceeds limit")
		}
		match := pattern.FindStringSubmatch(line)
		if match != nil {
			key := names[match[1]]
			if key == "" || !runOpen || ended {
				return a.result, errors.New("unexpected XCTest case")
			}
			if match[2] == "started." {
				if started[key] {
					return a.result, errors.New("duplicate XCTest start")
				}
				started[key] = true
				continue
			}
			if !started[key] {
				return a.result, errors.New("XCTest terminal without start")
			}
			action := "pass"
			if strings.HasPrefix(match[2], "failed") {
				action = "fail"
				runFailures++
			}
			if strings.HasPrefix(match[2], "skipped") {
				action = "skip"
			}
			if err := a.add(key, action); err != nil {
				return a.result, err
			}
		}
		if strings.HasPrefix(line, "Test Suite 'All tests' passed at ") || strings.HasPrefix(line, "Test Suite 'All tests' failed at ") || strings.HasPrefix(line, "Test Suite 'Selected tests' passed at ") || strings.HasPrefix(line, "Test Suite 'Selected tests' failed at ") {
			if ended || !runOpen {
				return a.result, errors.New("duplicate XCTest run terminal")
			}
			ended = true
			runOpen = false
			if strings.Contains(line, "' failed at ") != (runFailures > 0) {
				return a.result, errors.New("XCTest suite/case outcomes conflict")
			}
		}
	}
	if s.Err() != nil || !ended {
		return a.result, errors.New("incomplete XCTest run")
	}
	result, err := a.finish()
	result.Complete = err == nil
	return result, err
}

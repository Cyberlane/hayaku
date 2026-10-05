package native

import (
	"strings"
	"testing"
)

func TestXCTestIndependentCasesSkipsAndIncompleteRuns(t *testing.T) {
	expected := []Case{{Unit: "swift:A", ID: JUnitID("A.Tests", "testPass")}, {Unit: "swift:A", ID: JUnitID("A.Tests", "testSkip")}}
	text := "Test Suite 'All tests' started at timestamp.\nTest Case '-[A.Tests testPass]' started.\nTest Case '-[A.Tests testPass]' passed (0.001 seconds).\nTest Case '-[A.Tests testSkip]' started.\nTest Case '-[A.Tests testSkip]' skipped (0.001 seconds).\nTest Suite 'All tests' passed at timestamp.\n"
	r, err := ReconcileXCTest(strings.NewReader(text), expected)
	if err != nil || !r.Complete || r.Failed || len(r.Tests) != 2 || r.Tests[1].Action != "skip" {
		t.Fatalf("lost complete pass/skip: %+v %v", r, err)
	}
	for _, bad := range []string{strings.Replace(text, "Test Case '-[A.Tests testPass]' started.\n", "", 1), strings.Replace(text, "testSkip", "testUnknown", -1), strings.Replace(text, "Test Suite 'All tests' passed at timestamp.\n", "", 1), text + "Test Case '-[A.Tests testPass]' passed (0.001 seconds).\n", strings.Replace(text, "skipped (0.001 seconds).", "cancelled.", 1), strings.Replace(text, "testSkip", "testPass", -1)} {
		if r, err := ReconcileXCTest(strings.NewReader(bad), expected); err == nil || r.Complete {
			t.Fatal("incomplete/ambiguous XCTest run accepted")
		}
	}
	failed := strings.Replace(strings.Replace(text, "testPass]' passed", "testPass]' failed", 1), "'All tests' passed at", "'All tests' failed at", 1)
	r, err = ReconcileXCTest(strings.NewReader(failed), expected)
	if err != nil || !r.Complete || !r.Failed {
		t.Fatal("completed failure lost", err)
	}
}

package native

import (
	"strings"
	"testing"
)

func TestJUnitCompleteInventoryCountersAndNoDiagnostics(t *testing.T) {
	xml := `<testsuites tests="3" failures="1" errors="0" skipped="1"><testsuite name="Suite" tests="3" failures="1" errors="0" skipped="1"><properties><property name="environment" value="private-value"/></properties><testcase classname="Class" name="pass"/><testcase classname="Class" name="fail"><failure message="private value">private stack/source</failure></testcase><testcase classname="Class" name="skip"><skipped/></testcase><system-out>private stdout</system-out></testsuite></testsuites>`
	expected := []Case{{Unit: "unit", ID: JUnitID("Class", "pass")}, {Unit: "unit", ID: JUnitID("Class", "fail")}, {Unit: "unit", ID: JUnitID("Class", "skip")}}
	result, err := ReconcileJUnit(strings.NewReader(xml), expected)
	if err != nil || !result.Failed || len(result.Tests) != 3 || len(result.Units) != 1 || result.Units[0].Action != "fail" {
		t.Fatalf("bad JUnit result %+v %v", result, err)
	}
	for _, bad := range []string{strings.Replace(xml, `tests="3"`, `tests="4"`, 1), strings.Replace(xml, `name="pass"`, `name="unexpected"`, 1), strings.Replace(xml, `<testcase classname="Class" name="pass"/>`, "", 1), strings.Replace(xml, `<testcase classname="Class" name="pass"/>`, `<testcase classname="Class" name="pass"/><testcase classname="Class" name="pass"/>`, 1), strings.Replace(xml, `<skipped/>`, `<skipped/><failure/>`, 1), strings.Replace(xml, `<testcase classname="Class" name="pass"/>`, `<testcase classname="Class" name="pass" status="running"/>`, 1), `<!DOCTYPE testsuite [<!ENTITY secret SYSTEM "file:///private">]>` + xml, xml + `<testsuite/>`, strings.Replace(xml, `name="pass"`, `name="pass" name="other"`, 1), strings.Replace(xml, `<skipped/>`, `<rerunFailure/>`, 1)} {
		if _, err := ReconcileJUnit(strings.NewReader(bad), expected); err == nil {
			t.Fatal("ambiguous/incomplete JUnit accepted")
		}
	}
}
func trxFixture() string {
	return `<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010"><Results><UnitTestResult testId="id-pass" outcome="Passed"><Output><StdOut>private log</StdOut></Output></UnitTestResult><UnitTestResult testId="id-skip" outcome="NotExecuted"/></Results><ResultSummary outcome="Completed"><Counters total="2" executed="1" passed="1" failed="0" notExecuted="1" error="0" aborted="0" pending="0"/></ResultSummary></TestRun>`
}
func TestTRXCompleteInventoryAndCounters(t *testing.T) {
	expected := []Case{{Unit: "suite", ID: "id-pass"}, {Unit: "suite", ID: "id-skip"}}
	result, err := ReconcileTRX(strings.NewReader(trxFixture()), expected)
	if err != nil || result.Failed || len(result.Tests) != 2 {
		t.Fatal("TRX terminal lost", err)
	}
	for _, bad := range []string{strings.Replace(trxFixture(), `total="2"`, `total="3"`, 1), strings.Replace(trxFixture(), `outcome="Passed"`, `outcome="InProgress"`, 1), strings.Replace(trxFixture(), `id-skip`, `unexpected`, 1), strings.Replace(trxFixture(), `pending="0"`, `pending="1"`, 1), strings.Replace(trxFixture(), `schemas/VisualStudio`, `schemas/Unexpected`, 1), strings.Replace(trxFixture(), `<UnitTestResult testId="id-skip" outcome="NotExecuted"/>`, "", 1), strings.Replace(trxFixture(), `</ResultSummary>`, `<RunInfos><RunInfo outcome="Error"><Text>private</Text></RunInfo></RunInfos></ResultSummary>`, 1)} {
		if _, err := ReconcileTRX(strings.NewReader(bad), expected); err == nil {
			t.Fatal("incomplete TRX report accepted")
		}
	}
}
func TestXMLResourceBounds(t *testing.T) {
	expected := []Case{{Unit: "u", ID: JUnitID("C", "t")}}
	for _, bad := range []string{strings.Repeat("<testsuite>", 50) + strings.Repeat("</testsuite>", 50), strings.Repeat(" ", MaxResultBytes+1), `<?unsafe execute?><testsuite/>`} {
		if _, err := ReconcileJUnit(strings.NewReader(bad), expected); err == nil {
			t.Fatal("unbounded XML accepted")
		}
	}
}

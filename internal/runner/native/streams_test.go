package native

import (
	"strings"
	"testing"
)

func swiftFixture() string {
	return `{"version":0,"kind":"test","payload":{"kind":"function","id":"function","name":"test","isParameterized":false}}
{"version":0,"kind":"event","payload":{"kind":"runStarted"}}
{"version":0,"kind":"event","payload":{"kind":"testStarted","testID":"function"}}
{"version":0,"kind":"event","payload":{"kind":"testEnded","testID":"function"}}
{"version":0,"kind":"event","payload":{"kind":"runEnded"}}
`
}
func TestSwiftTerminalInventoryAndCrashRejection(t *testing.T) {
	expected := []Case{{Unit: "suite", ID: "function"}}
	result, err := ReconcileSwift(strings.NewReader(swiftFixture()), expected)
	if err != nil || result.Failed || len(result.Tests) != 1 {
		t.Fatal("Swift terminal missing", err)
	}
	for _, bad := range []string{strings.TrimSuffix(swiftFixture(), "\n"), strings.Replace(swiftFixture(), `"version":0`, `"version":"7.0"`, 1), strings.Replace(swiftFixture(), `{"version":0,"kind":"event","payload":{"kind":"runEnded"}}`+"\n", "", 1), strings.Replace(swiftFixture(), `"kind":"testEnded"`, `"kind":"testCancelled"`, 1), strings.Replace(swiftFixture(), `"kind":"testEnded"`, `"kind":"unexpected"`, 1), strings.Replace(swiftFixture(), `"id":"function"`, `"id":"different"`, 1)} {
		if _, err := ReconcileSwift(strings.NewReader(bad), expected); err == nil {
			t.Fatal("incomplete Swift stream accepted")
		}
	}
	issue := `{"version":0,"kind":"event","payload":{"kind":"issueRecorded","testID":"function","issue":{"isKnown":false},"messages":[{"text":"private source"}]}}` + "\n"
	failed := strings.Replace(swiftFixture(), `{"version":0,"kind":"event","payload":{"kind":"testEnded","testID":"function"}}`, issue+`{"version":0,"kind":"event","payload":{"kind":"testEnded","testID":"function"}}`, 1)
	result, err = ReconcileSwift(strings.NewReader(failed), expected)
	if err != nil || !result.Failed || result.Tests[0].Action != "fail" {
		t.Fatal("Swift issue became success", err)
	}
	unhandled := strings.Replace(failed, `"testID":"function","issue"`, `"issue"`, 1)
	result, err = ReconcileSwift(strings.NewReader(unhandled), expected)
	if err == nil || result.UnhandledErrors != 1 {
		t.Fatal("unhandled Swift issue accepted")
	}
}
func libtestFixture() string {
	return `{"type":"suite","event":"started","test_count":2}
{"type":"test","event":"started","name":"a"}
{"type":"test","event":"ok","name":"a"}
{"type":"test","event":"ignored","name":"b"}
{"type":"suite","event":"ok","passed":1,"failed":0,"ignored":1,"measured":0,"filtered_out":0,"exec_time":0.1}
`
}
func TestRustLibtestCompleteAndFailedResults(t *testing.T) {
	expected := []Case{{Unit: "binary", ID: "a"}, {Unit: "binary", ID: "b"}}
	result, err := ReconcileLibtest(strings.NewReader(libtestFixture()), expected)
	if err != nil || result.Failed || len(result.Tests) != 2 {
		t.Fatal("Rust terminal missing", err)
	}
	for _, bad := range []string{strings.Replace(libtestFixture(), `"test_count":2`, `"test_count":3`, 1), strings.Replace(libtestFixture(), `"passed":1`, `"passed":2`, 1), strings.Replace(libtestFixture(), `"name":"b"`, `"name":"unexpected"`, 1), strings.TrimSuffix(libtestFixture(), "\n"), strings.Replace(libtestFixture(), `{"type":"test","event":"started","name":"a"}`+"\n", "", 1), strings.Replace(libtestFixture(), `"measured":0`, `"measured":1`, 1)} {
		if _, err := ReconcileLibtest(strings.NewReader(bad), expected); err == nil {
			t.Fatal("incomplete Rust stream accepted")
		}
	}
	failed := strings.Replace(libtestFixture(), `"event":"ok","name":"a"`, `"event":"failed","name":"a","stdout":"private source"`, 1)
	failed = strings.Replace(failed, `"event":"ok","passed":1,"failed":0`, `"event":"failed","passed":0,"failed":1`, 1)
	result, err = ReconcileLibtest(strings.NewReader(failed), expected)
	if err != nil || !result.Failed {
		t.Fatal("Rust test failure lost", err)
	}
}

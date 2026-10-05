package adapter

import (
	"encoding/json"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	nativeresults "github.com/Cyberlane/hayaku/internal/runner/native"
)

// Real native fixtures exercise discovery/execution. This contract harness
// additionally checks every wire decoder against the same outcome scenarios.
func TestRunnerDecodersConformToCommonOutcomeContract(t *testing.T) {
	for _, id := range []string{"go", "vitest", "node-test", "unittest", "pytest", "jest", "playwright"} {
		t.Run(id, func(t *testing.T) {
			for _, action := range []string{"pass", "fail", "skip"} {
				t.Run(action, func(t *testing.T) {
					unit := model.Unit{ID: "w:unit", Workspace: "w", Selector: "a", Kind: id + "-file"}
					var raw []byte
					switch id {
					case "go":
						unit.Kind = "go-package"
						raw = []byte("{\"Action\":\"" + action + "\",\"Package\":\"a\",\"Test\":\"same\"}\n{\"Action\":\"" + action + "\",\"Package\":\"a\"}\n")
					case "vitest":
						unit.ID = "w:project:a"
						state := map[string]string{"pass": "passed", "fail": "failed", "skip": "skipped"}[action]
						raw = []byte(`{"schema":1,"complete":true,"errors":0,"files":[{"project":"project","file":"a","action":"` + state + `"}],"tests":[{"project":"project","file":"a","test":"[\"same\",0]","action":"` + state + `"}]}`)
					default:
						raw, _ = json.Marshal(nativeresults.Report{Schema: 1, Runner: id, Version: "fixture", Complete: true, Units: []nativeresults.Terminal{{Selector: "a", Action: action}}, Tests: []nativeresults.Terminal{{Selector: "a", Test: "same", Action: action}}})
					}
					input := invocation{workspace: model.Workspace{ID: "w", Adapter: id}, units: []model.Unit{unit}, options: RunOptions{Version: "fixture"}}
					result, err := providers[id].runner.decode(process.Output{Stdout: raw}, input)
					if err != nil || result.Outcomes == nil {
						t.Fatalf("decoder rejected native terminal: %v", err)
					}
					exit := 0
					if action == "fail" {
						exit = 1
					}
					if err := runner.Validate(*result.Outcomes, input.units, true, exit); err != nil {
						t.Fatal(err)
					}
					if runner.Failed(*result.Outcomes) != (action == "fail") {
						t.Fatal("normalized failure lost")
					}
					if len(result.Outcomes.Units) != 1 || result.Outcomes.Units[0].Unit != unit.ID || len(result.Outcomes.Tests) != 1 || result.Outcomes.Tests[0].Unit != unit.ID {
						t.Fatal("bridge did not preserve inventory identities")
					}
					// Every native decoder must reject a truncated document/stream.
					broken, _ := providers[id].runner.decode(process.Output{Stdout: raw[:len(raw)/2]}, input)
					if broken.Outcomes != nil && broken.Outcomes.Complete {
						t.Fatal("truncated native result claimed complete")
					}
				})
			}
		})
	}
}

func TestRegistrationCannotEnableNativeOmission(t *testing.T) {
	for _, capability := range Capabilities() {
		if capability.ProductionOmission {
			t.Fatalf("unqualified adapter %s enabled native omission", capability.ID)
		}
	}
	if SupportsShadow(model.Workspace{Adapter: "unknown"}) || SupportsObserve(model.Workspace{Adapter: "unknown"}) {
		t.Fatal("unknown bridge acquired capabilities")
	}
	if SupportsShadow(model.Workspace{Adapter: "vitest"}) || SupportsObserve(model.Workspace{Adapter: "vitest"}) {
		t.Fatal("unbound runtime acquired native assurance")
	}
}

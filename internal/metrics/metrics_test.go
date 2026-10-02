package metrics

import "testing"

func fullRun() Run {
	return Run{Context: Context{SourceDigest: "source", InputsDigest: "inputs", RunnerDigest: "runner", EnvironmentDigest: "environment", Platform: "linux/arm64", SuiteSetDigest: "suites"}, WallNanos: 1000, Phases: []Phase{{ID: "setup", Kind: "other", StartNanos: 0, DurationNanos: 100}, {ID: "tests", Kind: "runner", StartNanos: 100, DurationNanos: 900}}, Suites: []SuiteEvidence{{ID: "one", Execution: "executed", Outcome: "passed"}, {ID: "two", Execution: "executed", Outcome: "passed"}}}
}

func selectedRun() Run {
	run := fullRun()
	run.WallNanos = 650
	run.Phases = []Phase{{ID: "setup", Kind: "other", StartNanos: 0, DurationNanos: 100}, {ID: "plan", Kind: "hayaku", StartNanos: 100, DurationNanos: 150}, {ID: "tests", Kind: "runner", StartNanos: 250, DurationNanos: 400}}
	run.Suites[1].Execution = "hayaku-reused"
	return run
}

func TestNetSavingsIncludeMeasuredPlanningCosts(t *testing.T) {
	result, err := Compare(fullRun(), selectedRun())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.NetSavingsNanos != 350 || result.GrossRunnerSavingsNanos != 500 || result.Candidate.HayakuOverheadNanos != 150 || result.Candidate.Executed != 1 || result.Candidate.HayakuReused != 1 || result.Candidate.UpstreamCached != 0 {
		t.Fatalf("incorrect net comparison: %+v", result)
	}
	regression := selectedRun()
	regression.WallNanos = 1100
	regression.Phases[1].DurationNanos = 600
	regression.Phases[2].StartNanos = 700
	result, err = Compare(fullRun(), regression)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.NetSavingsNanos != -100 {
		t.Fatalf("negative savings hidden: %+v", result)
	}
}

func TestUpstreamCacheCannotBeCreditedToHayaku(t *testing.T) {
	baseline, candidate := fullRun(), selectedRun()
	candidate.Suites[1].Execution = "upstream-cached"
	result, err := Compare(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.Reason != "upstream-cache-state-mismatch" || result.NetSavingsNanos != 0 {
		t.Fatalf("Turbo gain attributed to Hayaku: %+v", result)
	}
	baseline.Suites[1].Execution = "upstream-cached"
	result, err = Compare(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Candidate.UpstreamCached != 1 || result.Candidate.HayakuReused != 0 {
		t.Fatalf("matching cache state lost: %+v", result)
	}
}

func TestComparisonRejectsUnmatchedOrNonpassingExperiments(t *testing.T) {
	for _, scenario := range []string{"source", "inputs", "runner", "environment", "platform", "suite-context", "inventory", "baseline-reuse", "failed", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			baseline, candidate := fullRun(), selectedRun()
			switch scenario {
			case "source":
				candidate.Context.SourceDigest = "different"
			case "inputs":
				candidate.Context.InputsDigest = "different"
			case "runner":
				candidate.Context.RunnerDigest = "different"
			case "environment":
				candidate.Context.EnvironmentDigest = "different"
			case "platform":
				candidate.Context.Platform = "different"
			case "suite-context":
				candidate.Context.SuiteSetDigest = "different"
			case "inventory":
				candidate.Suites[0].ID = "different"
			case "baseline-reuse":
				baseline.Suites[1].Execution = "hayaku-reused"
			case "failed":
				candidate.Suites[0].Outcome = "failed"
			case "incomplete":
				baseline.Suites[0].Outcome = "incomplete"
			}
			result, err := Compare(baseline, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid || result.Reason == "" || result.NetSavingsNanos != 0 || result.GrossRunnerSavingsNanos != 0 {
				t.Fatalf("invalid experiment has savings: %+v", result)
			}
		})
	}
}

func TestMeasurementsRejectMalformedEvidenceAndOverflow(t *testing.T) {
	for _, scenario := range []string{"missing-context", "negative-wall", "wall-limit", "negative-phase", "overlap", "phase-bounds", "duplicate-phase", "phase-kind", "duplicate-suite", "execution", "outcome", "failed-reuse", "missing-suites", "missing-phases", "phase-overflow"} {
		t.Run(scenario, func(t *testing.T) {
			run := fullRun()
			switch scenario {
			case "missing-context":
				run.Context.InputsDigest = ""
			case "negative-wall":
				run.WallNanos = -1
			case "wall-limit":
				run.WallNanos = MaxDurationNanos + 1
			case "negative-phase":
				run.Phases[0].DurationNanos = -1
			case "overlap":
				run.Phases[1].StartNanos = 50
			case "phase-bounds":
				run.Phases[1].DurationNanos = 1000
			case "duplicate-phase":
				run.Phases[1].ID = run.Phases[0].ID
			case "phase-kind":
				run.Phases[0].Kind = "unknown"
			case "duplicate-suite":
				run.Suites[1].ID = run.Suites[0].ID
			case "execution":
				run.Suites[0].Execution = "inferred-cache-hit"
			case "outcome":
				run.Suites[0].Outcome = "unknown"
			case "failed-reuse":
				run.Suites[0].Execution = "hayaku-reused"
				run.Suites[0].Outcome = "failed"
			case "missing-suites":
				run.Suites = nil
			case "missing-phases":
				run.Phases = nil
			case "phase-overflow":
				run.Phases[1].DurationNanos = int64(^uint64(0) >> 1)
			}
			if _, err := Summarize(run); err == nil {
				t.Fatal("malformed evidence accepted")
			}
		})
	}
}

func TestPhaseAndSuiteOrderingDoNotChangeMeasurement(t *testing.T) {
	run := selectedRun()
	original, err := Summarize(run)
	if err != nil {
		t.Fatal(err)
	}
	run.Phases[0], run.Phases[2] = run.Phases[2], run.Phases[0]
	run.Suites[0], run.Suites[1] = run.Suites[1], run.Suites[0]
	reordered, err := Summarize(run)
	if err != nil {
		t.Fatal(err)
	}
	if original != reordered {
		t.Fatal("measurement depends on report ordering")
	}
	if run.Phases[0].ID != "tests" {
		t.Fatal("summary mutated caller phases")
	}
}

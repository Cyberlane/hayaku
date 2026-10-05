package suite

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/testutil"
)

func groups() []Group {
	return []Group{
		{Request: capsule.Request{Name: "billing", Module: testutil.WASIExit(0), Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("a", 64), Files: []capsule.File{{Path: "billing.json", Data: []byte("baseline")}}}},
		{Request: capsule.Request{Name: "formatting", Module: testutil.WASIExit(0), Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("b", 64)}},
	}
}

func TestGroupsReuseOnlyUnchangedCompleteEnvelopes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows receipt ACL backend unqualified")
	}
	for name, change := range map[string]func(*capsule.Request){
		"input": func(r *capsule.Request) { r.Files[0].Data = []byte("changed") },
		"new-file": func(r *capsule.Request) {
			r.Files = append(r.Files, capsule.File{Path: "new.json", Data: []byte("new")})
		},
		"deleted-file": func(r *capsule.Request) { r.Files = nil },
		"directory":    func(r *capsule.Request) { r.Files = append(r.Files, capsule.File{Path: "empty", Directory: true}) },
		"producer":     func(r *capsule.Request) { r.ProducerIdentity = strings.Repeat("c", 64) },
		"environment":  func(r *capsule.Request) { r.Env = []capsule.Variable{{Key: "MODE", Value: "changed"}} },
		"arguments":    func(r *capsule.Request) { r.Args = append(r.Args, "changed") },
		"random-seed":  func(r *capsule.Request) { r.Seed = strings.Repeat("d", 64) },
		"module": func(r *capsule.Request) {
			r.Module = append(r.Module[:len(r.Module)-10], 10, 9, 1, 7, 0, 1, 0x41, 0, 0x10, 0, 0x0b)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			options := Options{CacheDir: filepath.Join(t.TempDir(), "receipts")}
			inventory := groups()
			cold, err := Run(ctx, inventory, options)
			if err != nil || !cold.Complete || !cold.Passed || cold.Executed != 2 || cold.Reused != 0 {
				t.Fatalf("cold result=%+v err=%v", cold, err)
			}
			warm, err := Run(ctx, inventory, options)
			if err != nil || warm.Executed != 0 || warm.Reused != 2 {
				t.Fatalf("unchanged groups did not reuse: %+v err=%v", warm, err)
			}
			change(&inventory[0].Request)
			changed, err := Run(ctx, inventory, options)
			if err != nil || !changed.Passed || changed.Executed != 1 || changed.Reused != 1 || changed.Groups[0].Mode != "executed" || changed.Groups[1].Mode != "reused" {
				t.Fatalf("changed result=%+v err=%v", changed, err)
			}
		})
	}
}

func TestFailureNeverBecomesGreenOrHidesAnotherGroup(t *testing.T) {
	ctx := context.Background()
	inventory := groups()
	options := Options{CacheDir: filepath.Join(t.TempDir(), "receipts")}
	inventory[0].Request.Module = testutil.WASIExit(1)
	for i := 0; i < 2; i++ {
		result, err := Run(ctx, inventory, options)
		if err != nil || !result.Complete || result.Passed || len(result.Groups) != 2 || result.Groups[0].Mode != "executed" || !result.Groups[1].Passed {
			t.Fatalf("failed suite incorrectly reconciled: %+v err=%v", result, err)
		}
	}
}

func TestSharedInputLimitsAndCandidateFailureInvalidateReceipts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows receipt ACL backend unqualified")
	}
	for _, scenario := range []string{"shared-input", "limits", "candidate-failure"} {
		t.Run(scenario, func(t *testing.T) {
			inventory := groups()
			if scenario == "shared-input" {
				for i := range inventory {
					inventory[i].Request.Files = append(inventory[i].Request.Files, capsule.File{Path: "shared.json", Data: []byte("baseline")})
				}
			}
			options := Options{CacheDir: filepath.Join(t.TempDir(), "receipts")}
			if result, err := Run(context.Background(), inventory, options); err != nil || !result.Passed {
				t.Fatal("baseline failed", err)
			}
			wantExecuted, wantReused, wantPassed := 1, 1, true
			switch scenario {
			case "shared-input":
				for i := range inventory {
					inventory[i].Request.Files[len(inventory[i].Request.Files)-1].Data = []byte("changed")
				}
				wantExecuted, wantReused = 2, 0
			case "limits":
				inventory[0].Limits = capsule.DefaultLimits()
				inventory[0].Limits.OutputBytes--
			case "candidate-failure":
				inventory[0].Request.Module = testutil.WASIExit(1)
				wantPassed = false
			}
			result, err := Run(context.Background(), inventory, options)
			if err != nil || !result.Complete || result.Passed != wantPassed || result.Executed != wantExecuted || result.Reused != wantReused {
				t.Fatalf("changed envelope reconciled incorrectly: %+v err=%v", result, err)
			}
		})
	}
}

func TestWholeInventoryValidatedBeforeAnyExecution(t *testing.T) {
	for name, change := range map[string]func([]Group) []Group{
		"empty":            func(g []Group) []Group { return nil },
		"duplicate":        func(g []Group) []Group { g[1].Request.Name = g[0].Request.Name; return g },
		"missing-producer": func(g []Group) []Group { g[1].Request.ProducerIdentity = ""; return g },
		"escaping-input":   func(g []Group) []Group { g[1].Request.Files = []capsule.File{{Path: "../secret"}}; return g },
		"too-many":         func(g []Group) []Group { return make([]Group, MaxGroups+1) },
	} {
		t.Run(name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "receipts")
			result, err := Run(context.Background(), change(groups()), Options{CacheDir: cache})
			if err == nil || result.Complete || result.Passed || len(result.Groups) != 0 {
				t.Fatalf("invalid inventory executed: %+v err=%v", result, err)
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid inventory warmed a receipt cache")
			}
		})
	}
}

func TestTamperedReceiptExecutesAndAuditRunsEveryGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows receipt ACL backend unqualified")
	}
	options := Options{CacheDir: filepath.Join(t.TempDir(), "receipts")}
	ctx := context.Background()
	inventory := groups()
	first, err := Run(ctx, inventory, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.CacheDir, first.Groups[0].Key+".json"), []byte(`{"complete":true,"passed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := Run(ctx, inventory, options)
	if err != nil || !next.Passed || next.Executed != 1 || next.Reused != 1 {
		t.Fatalf("forged pass accepted: %+v err=%v", next, err)
	}
	options.NoReuse = true
	audit, err := Run(ctx, inventory, options)
	if err != nil || !audit.Passed || audit.Executed != 2 || audit.Reused != 0 {
		t.Fatalf("audit did not execute every group: %+v err=%v", audit, err)
	}
}

func TestCancellationAndDeniedImportsCannotPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Run(ctx, groups(), Options{})
	if err == nil || result.Complete || result.Passed {
		t.Fatal("cancelled suite became green")
	}
	inventory := groups()
	inventory[0].Request.Module = []byte("not-wasm")
	result, err = Run(context.Background(), inventory, Options{})
	if err == nil || result.Complete || result.Passed {
		t.Fatal("unsupported module became green")
	}
	inventory = groups()
	inventory[0].Request.Module = bytes.Replace(inventory[0].Request.Module, []byte("wasi_snapshot_preview1"), []byte("host_snapshot_preview1"), 1)
	result, err = Run(context.Background(), inventory, Options{})
	if err == nil || result.Complete || result.Passed || len(result.Groups) != 2 || !result.Groups[1].Passed {
		t.Fatal("denied host import passed or hid the independent group")
	}
}

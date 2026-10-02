package capsule

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withTable(module []byte, limits []byte) []byte {
	// wasmCommand's independent fixture has type+function sections ending at
	// offset18, followed by memory; table belongs between function and memory.
	output := append([]byte(nil), module[:18]...)
	output = append(output, section(4, append([]byte{1, 0x70}, limits...))...)
	return append(output, module[18:]...)
}

func tableGrowBody(delta uint64) []byte {
	// ref.null funcref; i32.const delta; table.grow0; compare result with -1.
	// Trap if a grow beyond the backend's fixed bound unexpectedly succeeds.
	output := append([]byte{0xd0, 0x70, 0x41}, uleb(delta)...)
	return append(output, 0xfc, 15, 0, 0x41, 0x7f, 0x47, 0x04, 0x40, 0, 0x0b)
}

func TestTableAndMemoryGrowthHaveDeterministicResourceBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits []byte
		grow   uint64
	}{
		{"unbounded-table", []byte{0, 0}, maxTableElements + 1},
		{"smaller-explicit-maximum", []byte{1, 0, 3}, 4},
		{"larger-explicit-maximum", append([]byte{1, 0}, uleb(maxTableElements+5000)...), maxTableElements + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := withTable(wasmCommand("", "", nil, nil, false, tableGrowBody(test.grow), nil), test.limits)
			result, err := Run(context.Background(), validRequest(module), Options{})
			if err != nil || !result.Passed || !result.Qualified {
				t.Fatalf("table limit=%+v err=%v", result, err)
			}
		})
	}
	// One initial page + growth by two exceeds the configured two-page bound.
	body := []byte{0x41, 2, 0x40, 0, 0x41, 0x7f, 0x47, 0x04, 0x40, 0, 0x0b}
	result, err := Run(context.Background(), validRequest(wasmCommand("", "", nil, nil, false, body, nil)), Options{Limits: Limits{MemoryPages: 2}})
	if err != nil || !result.Passed || !result.Qualified {
		t.Fatalf("memory limit=%+v err=%v", result, err)
	}
}

func TestImportedMemoryGlobalsTablesAndOversizedModulesRejected(t *testing.T) {
	for _, kind := range []byte{1, 2, 3} {
		module := wasmCommand("", "", nil, nil, false, nil, nil)
		imports := append([]byte{1}, nameBytes("outside")...)
		imports = append(imports, nameBytes("shared")...)
		imports = append(imports, kind)
		// These are valid descriptor shapes even though they will be rejected
		// before the interpreter can link them to any external host state.
		switch kind {
		case 1:
			imports = append(imports, 0x70, 0, 1)
		case 2:
			imports = append(imports, 0, 1)
		case 3:
			imports = append(imports, 0x7f, 0)
		}
		modified := append([]byte(nil), module[:14]...)
		modified = append(modified, section(2, imports)...)
		modified = append(modified, module[14:]...)
		if result, err := Run(context.Background(), validRequest(modified), Options{}); err == nil || result.Qualified || result.Passed {
			t.Fatalf("external descriptor kind%d accepted: %+v %v", kind, result, err)
		}
	}
	module := withTable(wasmCommand("", "", nil, nil, false, nil, nil), append([]byte{0}, uleb(maxTableElements+1)...))
	if _, err := boundModuleTables(module); err == nil {
		t.Fatal("unbounded initial table accepted")
	}
	for _, malformed := range [][]byte{
		[]byte("not-WASM"),
		append(wasmCommand("", "", nil, nil, false, nil, nil), 4, 255, 255, 255, 255, 31),
		append(wasmCommand("", "", nil, nil, false, nil, nil), 4, 20, 1),
	} {
		if _, err := boundModuleTables(malformed); err == nil {
			t.Fatal("malformed module bounds accepted")
		}
	}
}

func TestFailedAuditInvalidatesPreviouslyAuthenticatedPass(t *testing.T) {
	module := wasmCommand("wasi_snapshot_preview1", "proc_exit", []byte{0x7f}, []uint64{1}, false, nil, nil)
	request := validRequest(module)
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	result, err := Run(context.Background(), request, options)
	if err != nil || result.Passed {
		t.Fatalf("failure fixture=%+v %v", result, err)
	}
	// Authenticate a stale 'pass' with this test's trusted local key to
	// model a regression previously missed by the backend qualification.
	stale := result
	stale.Passed = true
	cache, err := openPassCache(options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.store(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	cache.root.Close()
	options.NoReuse = true
	audit, err := Run(context.Background(), request, options)
	if err != nil || audit.Passed || audit.Mode != "executed" || audit.ExitCode != 1 {
		t.Fatalf("failure audit=%+v %v", audit, err)
	}
	if _, err := os.Lstat(filepath.Join(options.CacheDir, result.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("failed audit left a reusable pass")
	}
}

func TestCancellationDuringLoopDoesNotPublishPass(t *testing.T) {
	module := wasmCommand("", "", nil, nil, false, []byte{0x03, 0x40, 0x0c, 0, 0x0b}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	defer timer.Stop()
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	result, err := Run(ctx, validRequest(module), options)
	if err == nil || result.Passed || result.Complete || result.Qualified {
		t.Fatalf("cancelled loop=%+v err=%v", result, err)
	}
	matches, _ := filepath.Glob(filepath.Join(options.CacheDir, "*.json"))
	if len(matches) != 0 {
		t.Fatal("cancelled loop published pass")
	}
}

func FuzzBoundedModuleParser(f *testing.F) {
	f.Add(wasmCommand("", "", nil, nil, false, nil, nil))
	f.Add(withTable(wasmCommand("", "", nil, nil, false, nil, nil), []byte{0, 0}))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		bounded, err := boundModuleTables(data)
		if err != nil {
			return
		}
		again, err := boundModuleTables(bounded)
		if err != nil || !bytes.Equal(bounded, again) {
			t.Fatal("module-bound transformation is not idempotent")
		}
	})
}

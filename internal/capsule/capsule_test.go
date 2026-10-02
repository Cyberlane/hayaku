package capsule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validRequest(module []byte) Request {
	return Request{Name: "fixture", Module: module, Args: []string{"fixture.wasm"}, ProducerIdentity: strings.Repeat("a", 64)}
}

// These fixtures construct the public WebAssembly binary format directly,
// independent of host enforcement and without requiring a downloaded compiler.
func uleb(value uint64) []byte {
	var out []byte
	for {
		part := byte(value & 127)
		value >>= 7
		if value != 0 {
			part |= 128
		}
		out = append(out, part)
		if value == 0 {
			return out
		}
	}
}

func nameBytes(name string) []byte { return append(uleb(uint64(len(name))), []byte(name)...) }

func section(id byte, data []byte) []byte {
	return append(append([]byte{id}, uleb(uint64(len(data)))...), data...)
}

func wasmCommand(namespace, function string, types []byte, params []uint64, result bool, body, data []byte) []byte {
	module := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	functionIndex := byte(0)
	typeIndex := byte(0)
	if function != "" {
		resultTypes := []byte{0}
		if result {
			resultTypes = []byte{1, 0x7f}
		}
		funcType := append(append([]byte{0x60}, uleb(uint64(len(types)))...), types...)
		funcType = append(funcType, resultTypes...)
		module = append(module, section(1, append(append([]byte{2}, funcType...), 0x60, 0, 0))...)
		imports := append([]byte{1}, nameBytes(namespace)...)
		imports = append(imports, nameBytes(function)...)
		imports = append(imports, 0, 0)
		module = append(module, section(2, imports)...)
		functionIndex, typeIndex = 1, 1
	} else {
		module = append(module, section(1, []byte{1, 0x60, 0, 0})...)
	}
	module = append(module, section(3, []byte{1, typeIndex})...)
	module = append(module, section(5, []byte{1, 0, 1})...)
	exports := append([]byte{2}, nameBytes("memory")...)
	exports = append(exports, 2, 0)
	exports = append(exports, nameBytes("_start")...)
	exports = append(exports, 0, functionIndex)
	module = append(module, section(7, exports)...)
	code := []byte{0}
	if function != "" {
		for i, param := range params {
			if types[i] == 0x7e {
				code = append(code, 0x42)
			} else {
				code = append(code, 0x41)
			}
			// Fixture constants are positive and need a zero sign byte when
			// their top signed-LEB bit would otherwise indicate a negative.
			encoded := uleb(param)
			if encoded[len(encoded)-1]&64 != 0 {
				encoded[len(encoded)-1] |= 128
				encoded = append(encoded, 0)
			}
			code = append(code, encoded...)
		}
		code = append(code, 0x10, 0)
		if result {
			code = append(code, 0x1a)
		}
	}
	code = append(append(code, body...), 0x0b)
	module = append(module, section(10, append(append([]byte{1}, uleb(uint64(len(code)))...), code...))...)
	if data != nil {
		segment := append([]byte{1, 0, 0x41, 0, 0x0b}, uleb(uint64(len(data)))...)
		module = append(module, section(11, append(segment, data...))...)
	}
	return module
}

func TestAuthenticatedReuseAndCompleteIdentityInvalidation(t *testing.T) {
	ctx := context.Background()
	request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
	request.Files = []File{{Path: "generated/output.json", Data: []byte("one")}, {Path: "source/input.txt", Data: []byte("two")}}
	request.Env = []Variable{{Key: "B", Value: "two"}, {Key: "A", Value: "one"}}
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	first, err := Run(ctx, request, options)
	if err != nil || !first.Passed || !first.Qualified || first.Mode != "executed" || first.CacheStatus != "stored" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := Run(ctx, request, options)
	if err != nil || second.Mode != "reused" || second.Key != first.Key || !second.Complete || !second.Passed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	reordered := request
	reordered.Files = []File{request.Files[1], request.Files[0]}
	reordered.Env = []Variable{request.Env[1], request.Env[0]}
	third, err := Run(ctx, reordered, options)
	if err != nil || third.Mode != "reused" || third.Key != first.Key {
		t.Fatalf("canonical identity=%+v err=%v", third, err)
	}
	for _, test := range []struct {
		name   string
		change func(*Request, *Options)
	}{
		{"module", func(r *Request, _ *Options) {
			r.Module = append(append([]byte(nil), r.Module...), section(0, append(nameBytes("new-source"), 0))...)
		}},
		{"generated-bytes", func(r *Request, _ *Options) { r.Files[0].Data = []byte("changed") }},
		{"file-name", func(r *Request, _ *Options) { r.Files[0].Path = "generated/other.json" }},
		{"absent-input", func(r *Request, _ *Options) {
			r.Files = append(r.Files, File{Path: "source/previously-absent", Data: []byte("added")})
		}},
		{"argument", func(r *Request, _ *Options) { r.Args = append(r.Args, "another") }},
		{"environment", func(r *Request, _ *Options) { r.Env[0].Value = "changed" }},
		{"producer", func(r *Request, _ *Options) { r.ProducerIdentity = strings.Repeat("b", 64) }},
		{"seed", func(r *Request, _ *Options) { r.Seed = strings.Repeat("c", 64) }},
		{"memory-limit", func(_ *Request, o *Options) { o.Limits.MemoryPages = 128 }},
		{"output-limit", func(_ *Request, o *Options) { o.Limits.OutputBytes = 2 << 20 }},
		{"operational-timeout", func(_ *Request, o *Options) { o.Limits.Timeout = 20 * time.Second }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			changed.Files = append([]File(nil), request.Files...)
			changed.Env = append([]Variable(nil), request.Env...)
			changedOptions := options
			test.change(&changed, &changedOptions)
			result, err := Run(ctx, changed, changedOptions)
			if err != nil || result.Mode != "executed" || result.Key == first.Key || !result.Passed {
				t.Fatalf("changed identity=%+v err=%v", result, err)
			}
		})
	}
}

func TestCacheTamperingForeignAuthorityAndAudit(t *testing.T) {
	request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	ctx := context.Background()
	initial, err := Run(ctx, request, options)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(options.CacheDir, initial.Key+".json")
	original, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, forged := range [][]byte{
		bytes.Replace(original, []byte(initial.StdoutDigest), []byte(strings.Repeat("b", 64)), 1),
		[]byte(`{"complete":true,"passed":true,"key":"` + initial.Key + `"}`),
		append(append([]byte(nil), original...), []byte("\n{}")...),
		bytes.Replace(original, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1),
	} {
		if err := os.WriteFile(name, forged, 0600); err != nil {
			t.Fatal(err)
		}
		result, err := Run(ctx, request, options)
		if err != nil || result.Mode != "executed" || !result.Passed || result.CacheStatus != "stored" {
			t.Fatalf("forged cache was not rerun: %+v %v", result, err)
		}
	}
	foreign := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	if _, err := Run(ctx, request, foreign); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign.CacheDir, initial.Key+".json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, request, foreign)
	if err != nil || result.Mode != "executed" || !result.Passed {
		t.Fatalf("foreign authority reused: %+v %v", result, err)
	}
	options.NoReuse = true
	audit, err := Run(ctx, request, options)
	if err != nil || audit.Mode != "executed" || audit.StdoutDigest != initial.StdoutDigest || !audit.Passed {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	// Simulate a trusted-backend regression by authenticating an incorrect
	// previous output with the test's own private authority. Fresh audit must
	// invalidate that pass and refuse to certify the mismatch.
	cache, err := openPassCache(options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	wrong := initial
	wrong.StdoutDigest = strings.Repeat("d", 64)
	if err := cache.store(ctx, wrong); err != nil {
		t.Fatal(err)
	}
	cache.root.Close()
	audit, err = Run(ctx, request, options)
	if err == nil || audit.Passed || audit.Qualified || audit.Reason != "audit-outcome-mismatch" {
		t.Fatalf("mismatched audit=%+v err=%v", audit, err)
	}
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatal("failed audit retained reusable pass")
	}
}

func TestCapabilityDenialAndUnsupportedImportsNeverCache(t *testing.T) {
	i32, i64 := byte(0x7f), byte(0x7e)
	for _, test := range []struct {
		name   string
		module []byte
	}{
		{"process-host", wasmCommand("env", "exec", nil, nil, false, nil, nil)},
		{"unknown-wasi", wasmCommand("wasi_snapshot_preview1", "future_host_escape", nil, nil, false, nil, nil)},
		{"socket", wasmCommand("wasi_snapshot_preview1", "sock_shutdown", []byte{i32, i32}, []uint64{0, 0}, true, nil, nil)},
		{"caught-write-errno", wasmCommand("wasi_snapshot_preview1", "fd_write", []byte{i32, i32, i32, i32}, []uint64{3, 0, 0, 0}, true, nil, nil)},
		{"mkdir", wasmCommand("wasi_snapshot_preview1", "path_create_directory", []byte{i32, i32, i32}, []uint64{3, 0, 0}, true, nil, nil)},
		{"symlink", wasmCommand("wasi_snapshot_preview1", "path_symlink", []byte{i32, i32, i32, i32, i32}, []uint64{0, 0, 3, 0, 0}, true, nil, nil)},
		{"readonly-path-open-requested-write", wasmCommand("wasi_snapshot_preview1", "path_open", []byte{i32, i32, i32, i32, i32, i64, i64, i32, i32}, []uint64{3, 0, 0, 0, 1, 64, 0, 0, 0}, true, nil, nil)},
		{"fd-renumber", wasmCommand("wasi_snapshot_preview1", "fd_renumber", []byte{i32, i32}, []uint64{3, 1}, true, nil, nil)},
		{"trap", wasmCommand("", "", nil, nil, false, []byte{0}, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
			result, err := Run(context.Background(), validRequest(test.module), options)
			if err == nil || result.Passed || result.Qualified || result.Mode != "executed" {
				t.Fatalf("unsupported execution qualified: %+v %v", result, err)
			}
			matches, _ := filepath.Glob(filepath.Join(options.CacheDir, "*.json"))
			if len(matches) != 0 {
				t.Fatal("denied execution published passing evidence")
			}
		})
	}
}

func TestCompleteFailureTimeoutCancellationAndOutputBound(t *testing.T) {
	failure := wasmCommand("wasi_snapshot_preview1", "proc_exit", []byte{0x7f}, []uint64{1}, false, nil, nil)
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	for i := 0; i < 2; i++ {
		result, err := Run(context.Background(), validRequest(failure), options)
		if err != nil || !result.Complete || !result.Qualified || result.Passed || result.ExitCode != 1 || result.Mode != "executed" {
			t.Fatalf("failure=%+v err=%v", result, err)
		}
	}
	loop := wasmCommand("", "", nil, nil, false, []byte{0x03, 0x40, 0x0c, 0, 0x0b}, nil)
	options.Limits.Timeout = 10 * time.Millisecond
	started := time.Now()
	result, err := Run(context.Background(), validRequest(loop), options)
	if err == nil || result.Passed || result.Complete || result.Qualified || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Run(ctx, validRequest(failure), options); err == nil || result.Passed {
		t.Fatal("pre-cancelled execution produced a pass")
	}
	data := make([]byte, 40)
	binary.LittleEndian.PutUint32(data[0:], 16)
	binary.LittleEndian.PutUint32(data[4:], 24)
	copy(data[16:], []byte("source contents not exported"))
	output := wasmCommand("wasi_snapshot_preview1", "fd_write", []byte{0x7f, 0x7f, 0x7f, 0x7f}, []uint64{1, 0, 1, 8}, true, nil, data)
	options.Limits = Limits{OutputBytes: 8}
	result, err = Run(context.Background(), validRequest(output), options)
	if err == nil || result.Passed || result.Qualified || result.Reason != "output-bound-exceeded" {
		t.Fatalf("output bound=%+v err=%v", result, err)
	}
	matches, _ := filepath.Glob(filepath.Join(options.CacheDir, "*.json"))
	if len(matches) != 0 {
		t.Fatal("incomplete/failed execution published passing evidence")
	}
}

func TestImmutableMemoryFSAndInputCaptureRejectLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "input.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := LoadFiles(context.Background(), root, []string{"nested/input.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "input.txt"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	guestFS := newMemoryFS(files)
	data, err := fsReadAll(guestFS, "nested/input.txt")
	if err != nil || string(data) != "before" {
		t.Fatalf("guest read host mutation: %s %v", data, err)
	}
	file, _ := guestFS.Open("nested/input.txt")
	info, _ := file.Stat()
	file.Close()
	if !info.ModTime().Equal(time.Unix(0, 0)) || info.Mode().Perm() != 0444 {
		t.Fatalf("host metadata leaked: %+v", info)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		if _, err := LoadFiles(context.Background(), root, []string{"escape"}); err == nil {
			t.Fatal("captured external symlink")
		}
		if err := os.Symlink("nested", filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFiles(context.Background(), root, []string{"alias/input.txt"}); err == nil {
			t.Fatal("captured internal directory alias")
		}
	}
	for _, name := range []string{"../secret", "/secret", "nested/../secret", "nested\\input.txt", "."} {
		if _, err := LoadFiles(context.Background(), root, []string{name}); err == nil {
			t.Fatalf("captured invalid path %q", name)
		}
	}
	if _, err := guestFS.Open("../../secret"); err == nil {
		t.Fatal("guest traversed outside envelope")
	}
}

func fsReadAll(files *memoryFS, name string) ([]byte, error) {
	file, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func TestValidationAndPrivateCacheFailuresExecuteWithoutReuse(t *testing.T) {
	base := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
	for _, request := range []Request{
		{Name: "fixture", Module: base.Module, Args: base.Args},
		{Name: "fixture", Module: base.Module, ProducerIdentity: base.ProducerIdentity},
		{Name: "fixture", Module: base.Module, ProducerIdentity: base.ProducerIdentity, Args: base.Args, Env: []Variable{{Key: "A", Value: "one"}, {Key: "A", Value: "two"}}},
		{Name: "fixture", Module: base.Module, ProducerIdentity: base.ProducerIdentity, Args: base.Args, Files: []File{{Path: "a"}, {Path: "a/file"}}},
		{Name: "fixture", Module: base.Module, ProducerIdentity: base.ProducerIdentity, Args: base.Args, Files: []File{{Path: "a"}, {Path: "a"}}},
	} {
		if _, err := Run(context.Background(), request, Options{}); err == nil {
			t.Fatal("accepted malformed request")
		}
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := Run(context.Background(), base, Options{CacheDir: directory})
		if err != nil || !result.Passed || result.Mode != "executed" || result.CacheStatus != "unavailable" {
			t.Fatalf("unsafe cache authorized reuse: %+v %v", result, err)
		}
	}
}

func TestSeedStreamAndFixedDiagnosticsDoNotExportInputs(t *testing.T) {
	one := &seededReader{seed: []byte("known seed")}
	two := &seededReader{seed: []byte("known seed")}
	left, right := make([]byte, 97), make([]byte, 97)
	one.Read(left)
	two.Read(right[:3])
	two.Read(right[3:])
	if !bytes.Equal(left, right) {
		t.Fatal("entropy depends on read chunking")
	}
	request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
	request.Env = []Variable{{Key: "SECRET", Value: "raw-secret-sentinel"}}
	request.Files = []File{{Path: "input.txt", Data: []byte("source-content-sentinel")}}
	result, err := Run(context.Background(), request, Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte("raw-secret-sentinel")) || bytes.Contains(data, []byte("source-content-sentinel")) || !reflect.DeepEqual(result.Gaps, []string{}) {
		t.Fatalf("report exposes raw inputs: %s", data)
	}
	expected := sha256.Sum256(nil)
	if result.StdoutDigest != hex.EncodeToString(expected[:]) {
		t.Fatal("empty stdout digest is incorrect")
	}
}

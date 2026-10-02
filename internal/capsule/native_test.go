package capsule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// compileWASI uses only the installed Go toolchain, with downloads and automatic
// toolchain installation disabled. Actual artifact bytes are always bound;
// compiler/source inputs also invalidate the receipt even if optimized output
// happens to be identical. This is not qualification of native Go behavior.
func compileWASI(t *testing.T, target string, sources []string) ([]byte, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before, err := LoadFiles(ctx, root, sources)
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("installed Go compiler is required for the WASI qualification fixture")
	}
	goTool, err = filepath.EvalSymlinks(goTool)
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := os.ReadFile(goTool)
	if err != nil {
		t.Fatal("Go compiler identity is unavailable")
	}
	output := filepath.Join(t.TempDir(), "suite.wasm")
	args := []string{"build", "-trimpath", "-buildvcs=false", "-o", output, target}
	if strings.HasPrefix(target, "./internal/graph") {
		args = []string{"test", "-c", "-trimpath", "-buildvcs=false", "-o", output, target}
	}
	command := exec.CommandContext(ctx, goTool, args...)
	command.Dir = root
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOWORK", "GOFLAGS":
			continue
		}
		command.Env = append(command.Env, variable)
	}
	command.Env = append(command.Env, "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOFLAGS=")
	if err := command.Run(); err != nil {
		t.Fatalf("installed Go WASI compilation failed: %v", err)
	}
	after, err := LoadFiles(ctx, root, sources)
	if err != nil {
		t.Fatal(err)
	}
	identities := func(files []File) string {
		var items []identityFile
		for _, file := range files {
			items = append(items, identityFile{Path: file.Path, Digest: digest(file.Data), Size: len(file.Data)})
		}
		data, _ := json.Marshal(items)
		return digest(data)
	}
	if identities(before) != identities(after) {
		t.Fatal("fixture source changed during compilation")
	}
	module, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	producer := sha256.Sum256([]byte(digest(compiler) + "\x00" + identities(before) + "\x00" + strings.Join(args[:len(args)-2], "\x00")))
	return module, hex.EncodeToString(producer[:])
}

func TestRealHayakuGraphWASISuiteExecutesReusesAndAudits(t *testing.T) {
	module, producer := compileWASI(t, "./internal/graph", []string{"go.mod", "go.sum", "internal/graph/graph.go", "internal/graph/graph_test.go", "internal/model/model.go"})
	request := Request{Name: "Hayaku graph WASI suite", Module: module, ProducerIdentity: producer, Args: []string{"graph.test.wasm", "-test.v", "-test.count=1"}, Env: []Variable{{Key: "GOMAXPROCS", Value: "1"}}}
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	first, err := Run(context.Background(), request, options)
	if err != nil || !first.Complete || !first.Qualified || !first.Passed || first.StdoutBytes == 0 || first.Mode != "executed" {
		t.Fatalf("real graph suite=%+v err=%v", first, err)
	}
	second, err := Run(context.Background(), request, options)
	if err != nil || !second.Passed || second.Mode != "reused" || second.Key != first.Key {
		t.Fatalf("real graph reuse=%+v err=%v", second, err)
	}
	options.NoReuse = true
	audit, err := Run(context.Background(), request, options)
	if err != nil || !audit.Passed || audit.Mode != "executed" || audit.StdoutDigest != first.StdoutDigest || audit.StderrDigest != first.StderrDigest {
		t.Fatalf("real graph audit=%+v err=%v", audit, err)
	}
}

func TestRealGoWASIReadsOnlyDeclaredGeneratedFilesAndVirtualInputs(t *testing.T) {
	module, producer := compileWASI(t, "./internal/capsule/testdata/guest/main.go", []string{"go.mod", "go.sum", "internal/capsule/testdata/guest/main.go"})
	request := Request{Name: "Go WASI dynamic input fixture", Module: module, ProducerIdentity: producer, Args: []string{"guest.wasm", "deterministic"}, Env: []Variable{{Key: "VALUE", Value: "explicit"}}, Files: []File{{Path: "inputs/z.txt", Data: []byte("z")}, {Path: "inputs/read.txt", Data: []byte("value")}, {Path: "inputs/a.txt", Data: []byte("a")}}}
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	first, err := Run(context.Background(), request, options)
	if err != nil || !first.Passed || !first.Qualified || first.StdoutBytes == 0 {
		t.Fatalf("dynamic fixture=%+v err=%v", first, err)
	}
	options.NoReuse = true
	audit, err := Run(context.Background(), request, options)
	if err != nil || !audit.Passed || audit.StdoutDigest != first.StdoutDigest {
		t.Fatalf("virtual inputs were not deterministic: %+v err=%v", audit, err)
	}
	request.Seed = strings.Repeat("d", 64)
	changedSeed, err := Run(context.Background(), request, options)
	if err != nil || !changedSeed.Passed || changedSeed.StdoutDigest == first.StdoutDigest || changedSeed.Key == first.Key {
		t.Fatalf("explicit changed seed ignored: %+v err=%v", changedSeed, err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private host data"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAYAKU_HOST_ONLY_SECRET", "private host environment")
	request.Args = []string{"guest.wasm", "escape", filepath.ToSlash(outside)}
	escape, err := Run(context.Background(), request, options)
	if err != nil || !escape.Passed || !escape.Qualified {
		t.Fatalf("host data escaped into guest: %+v err=%v", escape, err)
	}
	request.Args = []string{"guest.wasm", "caught-write"}
	write, err := Run(context.Background(), request, options)
	if err == nil || write.Passed || write.Qualified || write.Reason != "filesystem-write-capability" {
		t.Fatalf("caught Go filesystem write certified: %+v err=%v", write, err)
	}
}

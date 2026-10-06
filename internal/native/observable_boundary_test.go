package native

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

func observableCacheSnapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][32]byte{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			t.Fatal("unexpected cache entry")
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// Include authentication material in the comparison without reporting it.
		result[entry.Name()] = sha256.Sum256(data)
	}
	return result
}

func TestObservableBoundaryVetoPreservesCacheBeforeAudit(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	prior := receipt{Schema: 1, Key: key, Cases: []string{"TestValue"}, Outcomes: model.Outcomes{
		Complete: true, Units: []model.Outcome{{Unit: "unit", Action: "pass"}},
		Tests: []model.Outcome{{Unit: "unit", Test: "TestValue", Action: "pass"}},
	}}
	if err := cache.Store(context.Background(), key, prior); err != nil {
		t.Fatal(err)
	}
	cache.Close()
	before := observableCacheSnapshot(t, cacheDir)
	if err := validateObservableEffects([]model.Effect{{Owner: "unit", Kind: "read", Path: "data/value"}}); err != nil {
		t.Fatal("healthy descriptive summary unavailable before faults", err)
	}
	faults := map[string]model.Effect{
		"clock":         {Owner: "unit", Kind: "clock"},
		"random":        {Owner: "unit", Kind: "random"},
		"metadata":      {Owner: "unit", Kind: "metadata", Path: "data/value"},
		"unknown":       {Owner: "unit", Kind: "unknown"},
		"missing-owner": {Kind: "read", Path: "data/value"},
		"missing-path":  {Owner: "unit", Kind: "read"},
		"escape-path":   {Owner: "unit", Kind: "read", Path: "../outside"},
		"lossy-owner":   {Owner: "unit\xff", Kind: "read", Path: "data/value"},
		"invented-kind": {Owner: "unit", Kind: "invented"},
		"clock-path":    {Owner: "unit", Kind: "clock", Path: "data/value"},
	}
	for name, effect := range faults {
		for _, audit := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/normal", true: "/audit"}[audit], func(t *testing.T) {
				artifact := Artifact{Name: "unit", Program: "/unavailable", Proof: strings.Repeat("b", 64), Cases: []string{"TestValue"}, Effects: []model.Effect{effect}}
				result, err := Execute(context.Background(), Backend{Identity: strings.Repeat("c", 64)}, artifact, cacheDir, audit, func(process.Output, Artifact) (model.Outcomes, error) {
					t.Fatal("observable veto reached native decoder")
					return model.Outcomes{}, nil
				})
				// The specific veto and empty key distinguish this from a failure
				// caused by the deliberately unavailable program or backend.
				if err == nil || !strings.Contains(err.Error(), "observable") || result.Qualified || result.Complete || result.Passed || result.Executed || result.Reused || result.Key != "" {
					t.Fatal("unsupported summary reached program/cache/audit authority")
				}
				if !reflect.DeepEqual(before, observableCacheSnapshot(t, cacheDir)) {
					t.Fatal("veto changed prior authenticated cache data")
				}
			})
		}
	}
}

func TestObservableBoundaryDormantCallsUseOriginalFullGroup(t *testing.T) {
	for name, dormant := range map[string]string{
		"metadata": "import \"os\"; func dormant(){_, _ = os.Stat(\"outside\")}",
		"clock":    "import \"time\"; func dormant(){_ = time.Now()}",
		"random":   "import \"crypto/rand\"; func dormant(){var data [8]byte; _, _ = rand.Read(data[:])}",
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string][]byte{
				"value.go":      []byte("package sample; func Value()int{return 1}"),
				"value_test.go": []byte("package sample; import \"testing\"; func TestAlpha(t *testing.T){if Value()!=1{t.Fatal(\"changed\")}}; func TestBeta(t *testing.T){if Value()!=1{t.Fatal(\"changed\")}}"),
			}
			if cases, err := qualifyGo(files); err != nil || !reflect.DeepEqual(cases, []string{"TestAlpha", "TestBeta"}) {
				t.Fatal("healthy independently expected pure inventory unavailable", err)
			}
			files["dormant.go"] = []byte("package sample; " + dormant)
			if _, err := qualifyGo(files); err == nil {
				t.Fatal("unexecuted observable call authorized native qualification")
			}
			root := t.TempDir()
			write(t, root, "go.mod", "module example.test/observable-fallback\ngo 1.26\n")
			for path, data := range files {
				write(t, root, path, string(data))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			for _, wantPass := range []bool{true, false} {
				if !wantPass {
					write(t, root, "value.go", "package sample; func Value()int{return 2}")
				}
				report, err := Go(ctx, root, Config{Schema: 1, Architecture: "arm64", Packages: []string{"."}}, "", false)
				if (err == nil) != wantPass || !report.Complete || report.Passed != wantPass || len(report.Groups) != 1 {
					t.Fatal("original full-group healthy/failing execution differs", err)
				}
				group := report.Groups[0]
				if group.Context != "original-host" || group.Qualified || group.Reused || !group.Executed || !group.Complete || group.Passed != wantPass {
					t.Fatal("dormant observable call escaped original full-group fallback")
				}
				action := "pass"
				if !wantPass {
					action = "fail"
				}
				want := []model.Outcome{{Unit: ".", Test: "TestAlpha", Action: action}, {Unit: ".", Test: "TestBeta", Action: action}}
				if !reflect.DeepEqual(group.Outcomes.Tests, want) {
					t.Fatal("full original scope lost independently expected case outcomes")
				}
			}
		})
	}
}

// This probe intentionally establishes the limitation of content containment.
// Successful observation is evidence against qualification, not a new provider.
func TestInstalledLinuxObservableBoundary(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("installed immutable Linux image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := prepareWorker(ctx, goTool, dir, arch)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	backend := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	// Check this exact image contains the outside object before accepting any
	// guest stat result or content-denial result as an oracle.
	exists, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--entrypoint=/bin/sh", image, "-c", "test -s /etc/passwd && printf 'outside-object-present\\n'")
	if err != nil || !exists.Completed || exists.ExitCode != 0 || string(exists.Stdout) != "outside-object-present\n" {
		t.Fatal("same-image outside metadata baseline unavailable", err)
	}
	source := `package main
import("crypto/rand";"fmt";"os";"time")
func main(){
 check:=func(name string,ok bool){if !ok{fmt.Println("FAILED",name);os.Exit(1)}}
 data,err:=os.ReadFile("data/value");check("declared-resource",err==nil&&string(data)=="fixture")
 _,err=os.ReadFile("/etc/passwd");check("outside-content-denied",err!=nil)
 info,err:=os.Stat("/etc/passwd");check("outside-metadata-observed",err==nil&&info.Mode().IsRegular()&&info.Size()>0)
 start:=time.Now();check("clock-observed",start.UnixNano()>0)
 advanced:=false;for i:=0;i<1000000;i++{if time.Now().After(start){advanced=true;break}};check("clock-advances",advanced)
 var random [32]byte;n,err:=rand.Read(random[:]);check("entropy-observed",err==nil&&n==len(random))
 fmt.Println("metadata-clock-random-observed")
}`
	write(t, dir, "probe.go", source)
	binary := filepath.Join(dir, "probe")
	if _, err := runGo(ctx, goTool, dir, buildEnvironment(arch), "build", "-trimpath", "-buildvcs=false", "-o", binary, filepath.Join(dir, "probe.go")); err != nil {
		t.Fatal(err)
	}
	request, _ := envelopeFixture(t)
	request.Env = []string{"GOMAXPROCS=1"}
	envelope := captureFixtureEnvelope(t, request)
	output, err := backend.execute(ctx, Artifact{Program: binary, Envelope: envelope})
	if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != "metadata-clock-random-observed\n" || len(output.Stderr) != 0 {
		t.Fatalf("contained healthy program failed observable probe: %s %v", output.Stdout, err)
	}
	// Observational success cannot qualify the same executable for reuse.
	result, err := Execute(ctx, backend, Artifact{Name: "observable-probe", Program: binary, Proof: strings.Repeat("a", 64), Cases: []string{"case"}, Envelope: envelope}, filepath.Join(t.TempDir(), "cache"), false, func(process.Output, Artifact) (model.Outcomes, error) {
		t.Fatal("operational observable probe reached receipt decoder")
		return model.Outcomes{}, nil
	})
	if err == nil || result.Qualified || result.Complete || result.Passed || result.Reused || result.Executed || result.Key != "" {
		t.Fatal("operational observable probe minted execution authority")
	}
	left, err := command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(left.Stdout)) != "" {
		t.Fatal("observable probe leaked native containers", err)
	}
}

package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestNativeEnvelopeCannotAuthorizeReceipts(t *testing.T) {
	request, _ := envelopeFixture(t)
	e := captureFixtureEnvelope(t, request)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	key := strings.Repeat("a", 64)
	if err := cache.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cacheDir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range []bool{false, true} {
		a := Artifact{Name: "fixture", Program: "/unavailable", Proof: key, Cases: []string{"case"}, Envelope: e}
		r, err := Execute(context.Background(), Backend{Identity: key}, a, cacheDir, audit, func(process.Output, Artifact) (model.Outcomes, error) {
			t.Fatal("unqualified envelope reached decoder")
			return model.Outcomes{}, nil
		})
		if err == nil || r.Qualified || r.Complete || r.Passed || r.Reused || r.Executed || r.Key != "" {
			t.Fatalf("envelope claimed execution authority: %+v %v", r, err)
		}
		after, err := os.ReadFile(filepath.Join(cacheDir, key+".json"))
		if err != nil || string(after) != string(before) {
			t.Fatal("rejection mutated prior cache authority")
		}
	}
}

func TestNativeEnvelopeArgumentValidationBeforeExecution(t *testing.T) {
	request, _ := envelopeFixture(t)
	e := captureFixtureEnvelope(t, request)
	for _, artifact := range []Artifact{
		{Args: []string{"bad\x00"}},
		{Args: []string{string([]byte{255})}},
		{Args: make([]string, 129)},
		{Env: []string{"A=one", "A=two"}},
		{Env: []string{"A"}},
		{Env: []string{"A=\x00"}},
		{Env: []string{"A=" + string([]byte{255})}},
		{Env: []string{"A=" + strings.Repeat("x", 65536)}},
		{Envelope: e, Env: []string{"DECLARED=conflict"}},
	} {
		// No installed backend/program is necessary to reject malformed input.
		output, err := (Backend{}).execute(context.Background(), artifact)
		if err == nil || output.Completed || !strings.Contains(err.Error(), "native") || err.Error() == "native program unavailable" {
			t.Fatal("malformed invocation accepted")
		}
	}
}

// This is operational enforcement acceptance only. The probe deliberately reads
// host metadata/time in its runtime and supplies no provider qualification.
func TestInstalledLinuxNativeEnvelope(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("installed immutable Linux image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	b := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := b.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	// Positive resources precede every denial oracle; a process that never starts
	// or cannot read its declared data cannot satisfy the expected marker.
	source := `package main
import("fmt";"os";"strings";"syscall";"time")
func main(){
 if len(os.Args)>1&&os.Args[1]=="exec-child"{fmt.Println("scratch-exec-bypass");return}
 check:=func(name string,ok bool){if !ok{fmt.Println("FAILED",name);os.Exit(1)}}
 data,err:=os.ReadFile("data/link");check("resource",err==nil&&string(data)=="fixture")
 link,err:=os.Readlink("data/link");check("relative-link",err==nil&&link=="value")
 entries,err:=os.ReadDir("data");check("membership",err==nil&&len(entries)==2&&entries[0].Name()=="link"&&entries[1].Name()=="value")
 _,err=os.Stat("data/missing");check("absence",os.IsNotExist(err))
 if len(os.Args)>1&&os.Args[1]=="empty-env"{check("empty-environment",len(os.Environ())==0)}else{check("environment",os.Getenv("DECLARED")=="fixture"&&os.Getenv("HAYAKU_ENVELOPE_AMBIENT")=="")}
 _,err=os.ReadFile("/etc/passwd");check("outside-read",err!=nil)
 _,err=os.ReadFile("/input/contract.json");check("control-read",err!=nil)
 _,err=os.ReadDir("/input");check("control-directory",err!=nil)
 _,err=os.ReadFile("/proc/self/environ");check("proc-read",err!=nil)
 err=os.WriteFile("data/value",[]byte("changed"),0600);check("input-write",err!=nil)
 err=os.WriteFile("/tmp/escaped",[]byte("changed"),0600);check("outside-write",err!=nil)
 if len(os.Args)>1&&os.Args[1]=="no-scratch"{err=os.WriteFile("/scratch/state",[]byte("new"),0600);check("write-denied",err!=nil);fmt.Println("envelope-probes-pass");return}
 _,err=os.Stat("/scratch/state");check("scratch-reset",os.IsNotExist(err))
 err=os.Mkdir("/scratch/dir",0700);check("scratch-directory",err==nil)
 err=os.WriteFile("/scratch/dir/value",[]byte("scratch"),0600);check("scratch-write",err==nil)
 data,err=os.ReadFile("/scratch/dir/value");check("scratch-read",err==nil&&string(data)=="scratch")
 err=os.Rename("/scratch/dir/value","/scratch/state");check("scratch-rename",err==nil)
 err=os.Symlink("/input/resources/data/value","/scratch/link");check("scratch-link-denied",err!=nil)
 err=syscall.Mkfifo("/scratch/fifo",0600);check("scratch-fifo-denied",err!=nil)
 data,err=os.ReadFile("/input/program");check("program-bytes",err==nil)
 err=os.WriteFile("/scratch/program",data,0700);check("scratch-program-copy",err==nil)
 err=syscall.Exec("/scratch/program",[]string{"/scratch/program","exec-child"},os.Environ());check("scratch-exec-denied",err!=nil)
 if len(os.Args)>1&&os.Args[1]=="fill"{err=os.WriteFile("/scratch/overflow",[]byte(strings.Repeat("x",16<<20)),0600);check("scratch-limit",err!=nil)}
 if len(os.Args)>1&&os.Args[1]=="wait"{fmt.Println("envelope-ready");for{time.Sleep(time.Second)}}
 if len(os.Args)>1&&os.Args[1]=="live-drift"{fmt.Println("envelope-ready");time.Sleep(5*time.Second)}
 fmt.Println("envelope-probes-pass")
}`
	write(t, dir, "probe.go", source)
	binary := filepath.Join(dir, "probe")
	if _, err := runGo(ctx, goTool, dir, buildEnvironment(arch), "build", "-trimpath", "-buildvcs=false", "-o", binary, filepath.Join(dir, "probe.go")); err != nil {
		t.Fatal(err)
	}
	request, root := envelopeFixture(t)
	if err := os.Symlink("value", filepath.Join(root, "data/link")); err != nil {
		t.Fatal(err)
	}
	request.Env = append(request.Env, "GOMAXPROCS=1")
	request.ScratchBytes = 8 << 20
	e := captureFixtureEnvelope(t, request)
	t.Setenv("HAYAKU_ENVELOPE_AMBIENT", "ambient-value")
	for _, arg := range []string{"normal", "normal", "fill"} {
		output, err := b.execute(ctx, Artifact{Program: binary, Envelope: e, Args: []string{arg}})
		if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != "envelope-probes-pass\n" || len(output.Stderr) != 0 {
			t.Fatalf("operational envelope: %s %s %v", output.Stdout, output.Stderr, err)
		}
	}
	request.ScratchBytes = 0
	denied := captureFixtureEnvelope(t, request)
	output, err := b.execute(ctx, Artifact{Program: binary, Envelope: denied, Args: []string{"no-scratch"}})
	if err != nil || !output.Completed || string(output.Stdout) != "envelope-probes-pass\n" {
		t.Fatalf("deny scratch: %s %v", output.Stdout, err)
	}
	request.ScratchBytes = 8 << 20
	request.Env = []string{}
	empty := captureFixtureEnvelope(t, request)
	output, err = b.execute(ctx, Artifact{Program: binary, Envelope: empty, Args: []string{"empty-env"}})
	if err != nil || !output.Completed || string(output.Stdout) != "envelope-probes-pass\n" {
		t.Fatalf("empty environment: %s %v", output.Stdout, err)
	}
	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	output, err = b.execute(bounded, Artifact{Program: binary, Envelope: e, Args: []string{"wait"}})
	stop()
	if err == nil || output.Completed || !strings.Contains(string(output.Stdout), "envelope-ready") {
		t.Fatalf("cancellation: %s %v", output.Stdout, err)
	}
	left, err := command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(left.Stdout)) != "" {
		t.Fatalf("native containers leaked: %s %v", left.Stdout, err)
	}
	write(t, root, "data/missing", "new")
	output, err = b.execute(ctx, Artifact{Program: binary, Envelope: e})
	if err == nil || output.Completed {
		t.Fatal("live absence drift executed")
	}
	if err := os.Remove(filepath.Join(root, "data/missing")); err != nil {
		t.Fatal(err)
	}
	t.Run("drift-during-successful-program", func(t *testing.T) {
		liveCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		type finished struct {
			output process.Output
			err    error
		}
		done := make(chan finished, 1)
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			output, err := b.execute(liveCtx, Artifact{Program: binary, Envelope: e, Args: []string{"live-drift"}})
			done <- finished{output, err}
		}()
		defer func() { stop(); <-returned }()
		ready := false
		for !ready {
			select {
			case outcome := <-done:
				t.Fatalf("program exited before live mutation: %s %v", outcome.output.Stdout, outcome.err)
			case <-liveCtx.Done():
				t.Fatal("live program never became ready")
			case <-time.After(100 * time.Millisecond):
			}
			containers, err := command(liveCtx, docker, "ps", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range strings.Fields(string(containers.Stdout)) {
				logs, err := command(liveCtx, docker, "logs", id)
				if err == nil && strings.Contains(string(logs.Stdout), "envelope-ready") {
					ready = true
					break
				}
			}
		}
		write(t, root, "data/value", "changed")
		outcome := <-done
		if outcome.err == nil || !strings.Contains(outcome.err.Error(), "declared input envelope changed") || outcome.output.Completed || outcome.output.ExitCode != 0 || !strings.Contains(string(outcome.output.Stdout), "envelope-probes-pass") {
			t.Fatalf("successful program concealed live drift: %+v %v", outcome.output, outcome.err)
		}
	})
	left, err = command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(left.Stdout)) != "" {
		t.Fatalf("native live-drift cleanup leaked containers: %s %v", left.Stdout, err)
	}
}

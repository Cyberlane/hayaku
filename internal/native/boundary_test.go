package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledLinuxBoundaryAdversaries(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("installed immutable image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	dir := t.TempDir()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	worker, err := prepareWorker(ctx, goTool, dir, arch)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	backend := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err = backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	source := `package main
 import("fmt";"os";"os/exec";"net";"syscall";"strings";"time")
 func main(){
  if len(os.Args)>1 && os.Args[1]=="--wait"{for{time.Sleep(time.Second)}}
  if len(os.Args)>1 && os.Args[1]=="--flood"{for{fmt.Println(strings.Repeat("x",65536))}}
  check:=func(name string,ok bool){if !ok{fmt.Println("FAILED",name);os.Exit(1)}}
  _,err:=os.ReadFile("/etc/passwd");check("outside-file",err!=nil)
  _,err=os.ReadDir("/proc/self/fd");check("descriptor-directory",err!=nil)
  _,err=os.ReadFile("/input/program");check("copied-input",err==nil)
  _,err=os.ReadFile("/input/authority.key");check("authority-absent",err!=nil)
  err=os.WriteFile("/input/program",[]byte("changed"),0600);check("input-write",err!=nil)
  err=os.WriteFile("/tmp/escaped",[]byte("changed"),0600);check("root-write",err!=nil)
  _,err=net.DialTimeout("tcp","127.0.0.1:1",time.Second);check("network",err!=nil)
  _,err=syscall.Socket(syscall.AF_UNIX,syscall.SOCK_STREAM,0);check("unix-socket",err!=nil)
  err=exec.Command("/bin/true").Run();check("subprocess",err!=nil)
  err=syscall.Exec("/bin/true",[]string{"true"},nil);check("foreign-exec",err!=nil)
  err=syscall.Exec("/input/boundary",[]string{"boundary","--version"},nil);check("boundary-reexec",err!=nil)
  check("environment",os.Getenv("HAYAKU_NATIVE_AMBIENT")=="" && os.Getenv("DECLARED")=="fixture")
  input,err:=os.ReadFile("/proc/self/environ");check("proc-environment",err!=nil && len(input)==0)
  fmt.Println("boundary-probes-pass")
 }`
	write(t, dir, "probe.go", source)
	binary := filepath.Join(dir, "probe")
	if _, err = runGo(ctx, goTool, dir, buildEnvironment(arch), "build", "-trimpath", "-buildvcs=false", "-o", binary, filepath.Join(dir, "probe.go")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAYAKU_NATIVE_AMBIENT", "fixture-ambient")
	a := Artifact{Program: binary, Env: []string{"DECLARED=fixture", "GOMAXPROCS=1"}}
	output, err := backend.execute(ctx, a)
	if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != "boundary-probes-pass\n" || len(output.Stderr) != 0 {
		t.Fatalf("boundary probes: %s %s %v", output.Stdout, output.Stderr, err)
	}
	t.Run("cancellation", func(t *testing.T) {
		a.Args = []string{"--wait"}
		bounded, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		output, err := backend.execute(bounded, a)
		if err == nil || output.Completed {
			t.Fatalf("cancelled process accepted %+v %v", output, err)
		}
	})
	t.Run("output-overflow", func(t *testing.T) {
		a.Args = []string{"--flood"}
		output, err := backend.execute(ctx, a)
		if err == nil || output.Completed {
			t.Fatalf("unbounded output accepted %v", err)
		}
	})
	// All created containers, including interrupted attach sessions, are removed.
	left, err := command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(left.Stdout)) != "" {
		t.Fatalf("native containers leaked: %s %v", left.Stdout, err)
	}
}

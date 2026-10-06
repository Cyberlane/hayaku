package native

import (
	"context"
	"encoding/json"
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

const independentChildSyscalls = `
func check(name string, ok bool){if !ok{fmt.Fprintln(os.Stderr,"probe-failed:"+name);os.Exit(71)}}
func probes(denied bool){
 fd,err:=syscall.Socket(syscall.AF_UNIX,syscall.SOCK_STREAM,0);check("socket",(err!=nil)==denied);if err==nil{syscall.Close(fd)}
 fd,err=syscall.Socket(syscall.AF_INET,syscall.SOCK_STREAM,0);check("inet-socket",(err!=nil)==denied);if err==nil{syscall.Close(fd)}
 pair,err:=syscall.Socketpair(syscall.AF_UNIX,syscall.SOCK_STREAM,0);check("socketpair",(err!=nil)==denied);if err==nil{syscall.Close(pair[0]);syscall.Close(pair[1])}
 err=exec.Command(os.Args[0],"spawn-control").Run();check("fork-exec-own",(err!=nil)==denied)
 clone:=uintptr(220);if runtime.GOARCH=="amd64"{clone=56};pid,_,cloneErr:=syscall.RawSyscall6(clone,uintptr(syscall.SIGCHLD),0,0,0,0,0);if cloneErr==0&&pid==0{syscall.RawSyscall(syscall.SYS_EXIT,0,0,0);for{}};check("raw-process-clone",(cloneErr!=0)==denied);if cloneErr==0{var state syscall.WaitStatus;got,err:=syscall.Wait4(int(pid),&state,0,nil);check("clone-positive-join",err==nil&&got==int(pid)&&state.Exited()&&state.ExitStatus()==0)}
 rw,err:=syscall.Mmap(-1,0,4096,syscall.PROT_READ|syscall.PROT_WRITE,syscall.MAP_PRIVATE|syscall.MAP_ANON);check("rw-mapping",err==nil);rw[0]=1
 err=syscall.Mprotect(rw,syscall.PROT_READ|syscall.PROT_EXEC);check("executable-protection",(err!=nil)==denied);syscall.Munmap(rw)
 rx,err:=syscall.Mmap(-1,0,4096,syscall.PROT_READ|syscall.PROT_EXEC,syscall.MAP_PRIVATE|syscall.MAP_ANON);check("executable-mapping",(err!=nil)==denied);if err==nil{syscall.Munmap(rx)}
 number:=uintptr(279);if runtime.GOARCH=="amd64"{number=319};name:=[]byte{'p','r','o','b','e',0};r,_,errno:=syscall.RawSyscall(number,uintptr(unsafe.Pointer(&name[0])),0,0);runtime.KeepAlive(name);check("memfd",(errno!=0)==denied);if errno==0{syscall.Close(int(r))}
 if denied{
  for _,target:=range []string{"/input/program","/input/helper","/boundary"}{if target!=os.Args[0]{err=syscall.Exec(target,[]string{target,"spawn-control"},[]string{"GOMAXPROCS=1"});check("exec-other-role",err!=nil)}}
  pidfd,_,errno:=syscall.RawSyscall(434,uintptr(os.Getpid()),0,0);check("pidfd-open-control",errno==0);_,_,errno=syscall.RawSyscall(438,pidfd,1,0);syscall.Close(int(pidfd));check("pidfd-getfd",errno==syscall.EPERM)
 }
}
`

const independentChildMain = `package main
import("encoding/binary";"encoding/json";"fmt";"io";"os";"os/exec";"reflect";"runtime";"strings";"syscall";"time";"unsafe")
type reply struct{Complete bool ` + "`json:\"complete\"`" + `;ExitCode int ` + "`json:\"exit_code\"`" + `;Stdout,Stderr []byte}
func readReply(f *os.File)(reply,error){var r reply;var size [4]byte;if _,err:=io.ReadFull(f,size[:]);err!=nil{return r,err};n:=binary.LittleEndian.Uint32(size[:]);if n==0||n>400000{return r,fmt.Errorf("frame bounds")};data:=make([]byte,n);if _,err:=io.ReadFull(f,data);err!=nil{return r,err};err:=json.Unmarshal(data,&r);return r,err}
func helperReady(){until:=time.Now().Add(4*time.Second);for{if data,err:=os.ReadFile("/scratch/helper-ready");err==nil&&string(data)=="ready"{return};check("helper-readiness",time.Now().Before(until));time.Sleep(10*time.Millisecond)}}
func finish(request,read *os.File){request.Close();data,err:=io.ReadAll(io.LimitReader(read,1));check("broker-eof",err==nil&&len(data)==0);read.Close()}
func main(){
 if len(os.Args)>1&&os.Args[1]=="spawn-control"{fmt.Println("spawn-control");return}
 if len(os.Args)>1&&os.Args[1]=="outside-control"{probes(false);fmt.Println("outside-controls-pass");return}
 if len(os.Args)>1&&os.Args[1]=="changed-launch"{check("changed-main-argv-env",os.Args[0]=="changed"&&os.Getenv("CHANGED")=="fixture"&&os.Getenv("MAIN_ONLY")=="");syscall.Close(3);syscall.Close(4);fmt.Println("main-self-reexec-changed");return}
 check("initial-main-argv",len(os.Args)==2&&os.Args[0]=="/input/program")
 check("initial-main-env",reflect.DeepEqual(os.Environ(),[]string{"GOMAXPROCS=1","MAIN_ONLY=fixture"}))
 wd,err:=os.Getwd();check("main-cwd",err==nil&&wd=="/input/resources")
 data,err:=io.ReadAll(os.Stdin);check("main-empty-stdin",err==nil&&len(data)==0)
 data,err=os.ReadFile("data/value");check("main-resource",err==nil&&string(data)=="fixture")
 _,err=os.Stat("/scratch/shared");check("main-scratch-reset",os.IsNotExist(err));check("main-scratch-write",os.WriteFile("/scratch/shared",[]byte("parent"),0600)==nil)
 request,read:=os.NewFile(3,"request"),os.NewFile(4,"reply")
 mode:=os.Args[1]
 if mode=="main-reexec"{err=syscall.Exec("/input/program",[]string{"changed","changed-launch"},[]string{"GOMAXPROCS=1","CHANGED=fixture"});check("self-reexec",err==nil)}
 if mode=="denials"{probes(true)}
 if mode=="malformed"{_,err=request.Write([]byte{2});check("malformed-write",err==nil);_,err=readReply(read);check("malformed-rejected",err!=nil);finish(request,read);fmt.Println("malformed-observed");return}
 if mode=="missing-request"{fmt.Println("request-deadline-ready");_,err=readReply(read);check("request-deadline",err!=nil);finish(request,read);fmt.Println("request-deadline-observed");return}
 _,err=request.Write([]byte{1});check("run-write",err==nil)
 if mode=="extra-run"||mode=="active-eof"||mode=="reader-stopped"||mode=="broker-stopped"||mode=="broker-killed"||mode=="cancel"||mode=="live-drift"{
  helperReady();fmt.Println("managed-helper-ready")
  switch mode{case "extra-run":_,err=request.Write([]byte{1});check("extra-run-write",err==nil)
  case "active-eof":request.Close()
  case "reader-stopped":time.Sleep(7*time.Second)
  case "broker-stopped":check("stop-broker",syscall.Kill(-1,syscall.SIGSTOP)==nil)
  case "broker-killed":check("kill-broker",syscall.Kill(-1,syscall.SIGKILL)==nil)}
 }
 r,err:=readReply(read)
 if mode=="reader-stopped"||mode=="broker-killed"{check("broken-broker-reply",err!=nil);finish(request,read);fmt.Println("broken-reply-observed");return}
 check("reply-frame",err==nil)
 if mode=="extra-run"||mode=="active-eof"||mode=="helper-deadline"||mode=="stdout-overflow"||mode=="stderr-overflow"{check("incomplete-reply",!r.Complete);check("reply-output-bounds",len(r.Stdout)<=131072&&len(r.Stderr)<=131072);if mode=="helper-deadline"{check("helper-deadline-positive",strings.Contains(string(r.Stdout),"helper-ready"))};finish(request,read);fmt.Println("incomplete-reply-observed");return}
 if mode=="ignored-failure"{check("actual-helper-failure",r.Complete&&r.ExitCode==9);finish(request,read);fmt.Println("ignored-helper-failure");return}
 check("complete-success",r.Complete&&r.ExitCode==0)
 if mode=="maximum"{check("maximum-output",len(r.Stdout)==131072&&len(r.Stderr)==131072&&strings.Trim(string(r.Stdout),"x")==""&&strings.Trim(string(r.Stderr),"y")=="")
 }else if mode=="helper-reexec"{check("changed-helper-launch",string(r.Stdout)=="helper-self-reexec-changed\n"&&string(r.Stderr)=="helper-stderr\n")
 }else{check("helper-output",string(r.Stdout)=="helper-positive\n"&&string(r.Stderr)=="helper-stderr\n")}
 data,err=os.ReadFile("/scratch/child");check("shared-child-scratch",err==nil&&string(data)=="child")
 if mode=="ignored-denial"{_,err=syscall.Socket(syscall.AF_UNIX,syscall.SOCK_STREAM,0);check("observed-denial",err!=nil)}
 if mode=="late-extra-run"{_,err=request.Write([]byte{1});check("late-run-write",err==nil)}
 finish(request,read)
 if mode=="late-extra-run"{data,err=os.ReadFile("/scratch/launches");check("one-helper-launch",err==nil&&string(data)=="x")}
 fmt.Println("managed-protocol-pass")
}
` + independentChildSyscalls

const independentChildHelper = `package main
import("fmt";"io";"os";"os/exec";"reflect";"runtime";"strings";"syscall";"time";"unsafe")
func main(){
 if len(os.Args)>1&&os.Args[1]=="spawn-control"{fmt.Println("spawn-control");return}
 if len(os.Args)>1&&os.Args[1]=="changed-launch"{check("changed-helper-argv-env",os.Args[0]=="changed"&&os.Getenv("CHANGED")=="fixture"&&os.Getenv("CHILD_ONLY")=="");fmt.Println("helper-self-reexec-changed");return}
 check("initial-helper-argv",len(os.Args)==4&&os.Args[0]=="/input/helper"&&os.Args[1]=="fixed"&&os.Args[2]=="argument with spaces")
 check("initial-helper-env",reflect.DeepEqual(os.Environ(),[]string{"GOMAXPROCS=1","CHILD_ONLY=fixture"}))
 wd,err:=os.Getwd();check("helper-cwd",err==nil&&wd=="/input/resources")
 data,err:=io.ReadAll(os.Stdin);check("helper-empty-stdin",err==nil&&len(data)==0)
 data,err=os.ReadFile("data/value");check("helper-resource",err==nil&&string(data)=="fixture")
 data,err=os.ReadFile("/scratch/shared");check("shared-main-scratch",err==nil&&string(data)=="parent")
 launches,err:=os.OpenFile("/scratch/launches",os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);check("launch-record",err==nil);_,err=launches.Write([]byte("x"));check("launch-record-write",err==nil);check("launch-record-close",launches.Close()==nil)
 _,err=os.Stat("/scratch/child");check("helper-scratch-reset",os.IsNotExist(err));check("helper-scratch-write",os.WriteFile("/scratch/child",[]byte("child"),0600)==nil)
 check("helper-ready-marker",os.WriteFile("/scratch/helper-ready",[]byte("ready"),0600)==nil)
 mode:=os.Args[3]
 if mode=="maximum"{fmt.Fprint(os.Stdout,strings.Repeat("x",131072));fmt.Fprint(os.Stderr,strings.Repeat("y",131072));return}
 if mode=="stdout-overflow"{fmt.Fprint(os.Stdout,strings.Repeat("x",131073));return}
 if mode=="stderr-overflow"{fmt.Fprint(os.Stderr,strings.Repeat("y",131073));return}
 fmt.Fprintln(os.Stderr,"helper-stderr")
 if mode=="reexec"{err=syscall.Exec("/input/helper",[]string{"changed","changed-launch"},[]string{"GOMAXPROCS=1","CHANGED=fixture"});check("helper-self-reexec",err==nil)}
 if mode=="denials"{probes(true)}
 if mode=="wait"{fmt.Println("helper-ready");for{time.Sleep(time.Second)}}
 fmt.Println("helper-positive")
 if mode=="hold"{time.Sleep(3*time.Second)}
 if mode=="fail"{os.Exit(9)}
}
` + independentChildSyscalls

type independentChildFixture struct {
	backend              Backend
	main, helper, docker string
	request              EnvelopeRequest
}

func prepareIndependentChildFixture(t *testing.T, ctx context.Context) independentChildFixture {
	t.Helper()
	image, arch := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_NATIVE_ARCH")
	if image == "" {
		t.Skip("installed immutable Linux image required for actual managed-child probes")
	}
	if arch == "" {
		arch = "arm64"
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	worker, err := prepareWorker(ctx, goTool, dir, arch)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	fixture := independentChildFixture{backend: Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}, docker: docker}
	if err := fixture.backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	for _, program := range []struct{ name, source string }{{"main", independentChildMain}, {"helper", independentChildHelper}} {
		source, binary := filepath.Join(dir, program.name+".go"), filepath.Join(dir, program.name)
		if err := os.WriteFile(source, []byte(program.source), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := runGo(ctx, goTool, dir, buildEnvironment(arch), "build", "-trimpath", "-buildvcs=false", "-o", binary, source); err != nil {
			t.Fatal("independent managed-child fixture build failed")
		}
		if program.name == "main" {
			fixture.main = binary
		} else {
			fixture.helper = binary
		}
	}
	fixture.request, _ = envelopeFixture(t)
	fixture.request.Env = []string{"GOMAXPROCS=1", "MAIN_ONLY=fixture"}
	fixture.request.ScratchBytes = 8 << 20
	return fixture
}

func (f independentChildFixture) envelope(t *testing.T, mode string) *Envelope {
	t.Helper()
	r := f.request
	r.Child = &ChildRequest{Program: f.helper, Args: []string{"fixed", "argument with spaces", mode}, Env: []string{"GOMAXPROCS=1", "CHILD_ONLY=fixture"}}
	return captureFixtureEnvelope(t, r)
}

func assertManagedEnvelopeNoAuthority(t *testing.T, b Backend, main string, e *Envelope) {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	if err := cache.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	cache.Close()
	before := observableCacheSnapshot(t, cacheDir)
	for _, audit := range []bool{false, true} {
		decoded := false
		result, err := Execute(context.Background(), b, Artifact{Name: "managed", Program: main, Proof: key, Cases: []string{"case"}, Envelope: e}, cacheDir, audit, func(process.Output, Artifact) (model.Outcomes, error) {
			decoded = true
			return model.Outcomes{}, nil
		})
		if err == nil || result.Qualified || result.Complete || result.Passed || result.Executed || result.Reused || result.Key != "" || decoded || !reflect.DeepEqual(before, observableCacheSnapshot(t, cacheDir)) {
			t.Fatal("operational managed child gained receipt/cache/audit authority")
		}
	}
}

func assertNoManagedContainers(t *testing.T, docker string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(out.Stdout)) != "" {
		t.Fatal("managed-child containers or their PID trees remain after cleanup")
	}
}

func TestInstalledLinuxManagedChildIndependentProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f := prepareIndependentChildFixture(t, ctx)
	control, err := command(ctx, f.docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--pids-limit=64", "--mount", "type=bind,source="+f.main+",target=/control,readonly", "--entrypoint=/control", f.backend.Image, "outside-control")
	if err != nil || !control.Completed || control.ExitCode != 0 || string(control.Stdout) != "outside-controls-pass\n" || len(control.Stderr) != 0 {
		t.Fatal("outside-worker fork/exec/socket/memfd/executable-memory controls unavailable")
	}
	cases := []struct {
		main, helper, marker string
		timeout              time.Duration
		incomplete           bool
	}{
		{"positive", "positive", "managed-protocol-pass", 15 * time.Second, false},
		{"positive", "positive", "managed-protocol-pass", 15 * time.Second, false},
		{"maximum", "maximum", "managed-protocol-pass", 15 * time.Second, false},
		{"denials", "denials", "managed-protocol-pass", 15 * time.Second, false},
		{"main-reexec", "positive", "main-self-reexec-changed", 15 * time.Second, false},
		{"helper-reexec", "reexec", "managed-protocol-pass", 15 * time.Second, false},
		{"ignored-failure", "fail", "ignored-helper-failure", 15 * time.Second, false},
		{"ignored-denial", "positive", "managed-protocol-pass", 15 * time.Second, false},
		{"malformed", "positive", "malformed-observed", 15 * time.Second, false},
		{"extra-run", "hold", "incomplete-reply-observed", 15 * time.Second, false},
		{"late-extra-run", "positive", "managed-protocol-pass", 15 * time.Second, false},
		{"active-eof", "hold", "incomplete-reply-observed", 15 * time.Second, false},
		{"stdout-overflow", "stdout-overflow", "incomplete-reply-observed", 15 * time.Second, false},
		{"stderr-overflow", "stderr-overflow", "incomplete-reply-observed", 15 * time.Second, false},
		{"reader-stopped", "maximum", "broken-reply-observed", 15 * time.Second, false},
		{"broker-killed", "wait", "broken-reply-observed", 15 * time.Second, false},
		{"broker-stopped", "wait", "managed-helper-ready", 5 * time.Second, true},
		{"cancel", "wait", "managed-helper-ready", 5 * time.Second, true},
		{"missing-request", "positive", "request-deadline-observed", 40 * time.Second, false},
		{"helper-deadline", "wait", "incomplete-reply-observed", 40 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.main, func(t *testing.T) {
			e := f.envelope(t, tc.helper)
			bounded, stop := context.WithTimeout(ctx, tc.timeout)
			started := time.Now()
			out, err := f.backend.execute(bounded, Artifact{Program: f.main, Args: []string{tc.main}, Envelope: e})
			elapsed := time.Since(started)
			stop()
			if !strings.Contains(string(out.Stdout), tc.marker+"\n") || (tc.incomplete && (err == nil || out.Completed)) || (!tc.incomplete && (err != nil || !out.Completed || out.ExitCode != 0 || len(out.Stderr) != 0)) {
				t.Fatalf("actual child protocol failed: complete=%t exit=%d expected-marker=%t error=%v", out.Completed, out.ExitCode, strings.Contains(string(out.Stdout), tc.marker+"\n"), err)
			}
			if (tc.main == "missing-request" || tc.main == "helper-deadline") && elapsed < 28*time.Second {
				t.Fatal("deadline case ended before its fixed policy timeout")
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.main, e)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

// A successful main and helper do not conceal lasting host-source or private
// copied-helper drift. The worker has already started the captured helper when
// the independent host mutation happens, so the final guard must detect it.
func TestInstalledLinuxManagedChildIndependentLiveDrift(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := prepareIndependentChildFixture(t, ctx)
	for _, kind := range []string{"source", "copy"} {
		t.Run(kind, func(t *testing.T) {
			e := f.envelope(t, "hold")
			bounded, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			type finished struct {
				out process.Output
				err error
			}
			done := make(chan finished, 1)
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				out, err := f.backend.execute(bounded, Artifact{Program: f.main, Args: []string{"live-drift"}, Envelope: e})
				done <- finished{out, err}
			}()
			defer func() { stop(); <-returned }()
			id := ""
			for id == "" {
				select {
				case <-done:
					t.Fatal("managed child exited before independent live mutation")
				case <-bounded.Done():
					t.Fatal("managed helper did not become ready for mutation")
				case <-time.After(100 * time.Millisecond):
				}
				containers, err := command(bounded, f.docker, "ps", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
				if err != nil {
					t.Fatal(err)
				}
				for _, candidate := range strings.Fields(string(containers.Stdout)) {
					logs, err := command(bounded, f.docker, "logs", candidate)
					if err == nil && strings.Contains(string(logs.Stdout), "managed-helper-ready\n") {
						id = candidate
						break
					}
				}
			}
			if kind == "source" {
				original, err := os.ReadFile(f.helper)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(f.helper, original, 0755); err != nil {
						t.Error("restore independent helper source failed")
					}
				})
				if err := os.WriteFile(f.helper, append(original, byte(0)), 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				mounts, err := command(bounded, f.docker, "inspect", id, "--format", "{{json .Mounts}}")
				var entries []struct{ Source, Destination string }
				if err != nil || json.Unmarshal(mounts.Stdout, &entries) != nil {
					t.Fatal("private helper mount unavailable")
				}
				copy := ""
				for _, entry := range entries {
					if entry.Destination == "/input" {
						copy = filepath.Join(entry.Source, "helper")
					}
				}
				info, err := os.Stat(copy)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 || os.Chmod(copy, 0444) != nil {
					t.Fatal("independent copied-helper mode mutation failed")
				}
			}
			result := <-done
			expectedError := "declared input envelope changed"
			if kind == "copy" {
				expectedError = "native managed child copy changed"
			}
			if result.err == nil || !strings.Contains(result.err.Error(), expectedError) || result.out.Completed || result.out.ExitCode != 0 || len(result.out.Stderr) != 0 || !strings.Contains(string(result.out.Stdout), "managed-protocol-pass\n") {
				t.Fatalf("successful managed child concealed %s drift: completed=%t exit=%d error=%v", kind, result.out.Completed, result.out.ExitCode, result.err)
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.main, e)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/process"
)

// This separately authored C executable exercises the operational transport.
// Successful execution, including ignored denials, supplies no effect proof.
const independentStaticProgram = `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <time.h>
extern char **environ;
static void check(const char *name,int ok){if(!ok){fprintf(stderr,"probe-failed:%s errno=%d\n",name,errno);exit(71);}}
static void probes(int denied,const char *self){
 int fd=socket(AF_UNIX,SOCK_STREAM,0);check("unix-socket",denied ? fd<0&&errno==EPERM : fd>=0);if(fd>=0)close(fd);
 fd=socket(AF_INET,SOCK_STREAM,0);check("inet-socket",denied ? fd<0&&errno==EPERM : fd>=0);if(fd>=0)close(fd);
 int pair[2];int rc=socketpair(AF_UNIX,SOCK_STREAM,0,pair);check("socketpair",denied ? rc<0&&errno==EPERM : rc==0);if(rc==0){close(pair[0]);close(pair[1]);}
 pid_t pid=fork();check("fork",denied ? pid<0&&errno==EPERM : pid>=0);if(pid==0){char *args[]={(char*)self,"spawn-control",NULL};execv(self,args);_exit(73);}if(pid>0){int status;check("exec-positive-join",waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0);}
 void *rw=mmap(NULL,4096,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("rw-mapping",rw!=MAP_FAILED);((char*)rw)[0]=1;
 rc=mprotect(rw,4096,PROT_READ|PROT_EXEC);check("executable-protection",denied ? rc<0&&errno==EPERM : rc==0);munmap(rw,4096);
 void *rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("executable-mapping",denied ? rx==MAP_FAILED&&errno==EPERM : rx!=MAP_FAILED);if(rx!=MAP_FAILED)munmap(rx,4096);
 fd=syscall(SYS_memfd_create,"probe",0);check("memfd",denied ? fd<0&&errno==EPERM : fd>=0);if(fd>=0)close(fd);
 if(denied){char *args[]={"/boundary",NULL};execv(args[0],args);check("foreign-executable",errno==EPERM||errno==EACCES);}
}
static void emit(int fd,size_t total,char value){char block[8192];memset(block,value,sizeof(block));while(total){size_t n=total<sizeof(block)?total:sizeof(block);ssize_t written=write(fd,block,n);if(written<=0)_exit(74);total-=written;}}
int main(int argc,char **argv){
 if(argc>1&&!strcmp(argv[1],"spawn-control"))return 0;
 if(argc>1&&!strcmp(argv[1],"outside-control")){
  char cwd[4096];check("outside-cwd",getcwd(cwd,sizeof(cwd))!=NULL);
  struct stat st;check("outside-metadata",stat("/etc/os-release",&st)==0);
  int fd=open("/etc/os-release",O_RDONLY);char value;check("outside-content",fd>=0&&read(fd,&value,1)==1);close(fd);
  fd=open("/tmp/outside-control",O_CREAT|O_WRONLY,0600);check("outside-write",fd>=0&&write(fd,"x",1)==1);close(fd);
  probes(0,argv[0]);puts("outside-static-controls-pass");return 0;
 }
 if(argc>1&&!strcmp(argv[1],"changed-launch")){check("changed-args-env",argc==2&&!strcmp(argv[0],"changed")&&getenv("CHANGED")&&!strcmp(getenv("CHANGED"),"fixture")&&!getenv("DECLARED"));puts("static-self-reexec-changed");return 0;}
 check("initial-argv",argc==3&&!strcmp(argv[0],"/input/program")&&!strcmp(argv[2],"argument with spaces"));
 check("initial-env",environ[0]&&environ[1]&&!environ[2]&&!strcmp(environ[0],"DECLARED=fixture")&&!strcmp(environ[1],"FIXED=second"));
 char cwd[4096];check("initial-cwd",getcwd(cwd,sizeof(cwd))&&!strcmp(cwd,"/input/resources"));
 char data[16];check("empty-stdin",read(0,data,1)==0);for(int fd=3;fd<32;fd++){errno=0;check("closed-extra-fd",fcntl(fd,F_GETFD)==-1&&errno==EBADF);}
 int fd=open("data/value",O_RDONLY);check("resource",fd>=0&&read(fd,data,sizeof(data))==7&&!memcmp(data,"fixture",7));close(fd);
 struct stat st;check("resource-mode",stat("data/value",&st)==0&&(st.st_mode&0777)==0444);
 check("declared-absence",stat("data/missing",&st)<0&&errno==ENOENT);
 fd=open("data/value",O_WRONLY);check("resource-write-denial",fd<0&&(errno==EACCES||errno==EPERM||errno==EROFS));
 check("fresh-scratch",stat("/scratch/control",&st)<0&&errno==ENOENT);
 fd=open("/scratch/control",O_CREAT|O_WRONLY,0600);check("scratch-write",fd>=0&&write(fd,"x",1)==1);close(fd);
 const char *mode=argv[1];
 if(!strcmp(mode,"scratch-limits")){
  check("scratch-fifo-denial",mkfifo("/scratch/fifo",0600)<0&&(errno==EPERM||errno==EACCES));
  fd=open("/scratch/quota",O_CREAT|O_WRONLY,0600);check("quota-open",fd>=0);char block[8192];memset(block,'q',sizeof(block));size_t written=0;ssize_t n;
  while(written<9u*1024u*1024u){n=write(fd,block,sizeof(block));if(n<0)break;check("quota-progress",n>0);written+=n;}
  check("eight-MiB-scratch-bound",n<0&&errno==ENOSPC&&written>=7u*1024u*1024u&&written<=8u*1024u*1024u);close(fd);check("quota-remove",unlink("/scratch/quota")==0);
  int in=open("/input/program",O_RDONLY);check("captured-executable-read",in>=0);fd=open("/scratch/program",O_CREAT|O_WRONLY,0700);check("scratch-executable-copy",fd>=0);
  while((n=read(in,block,sizeof(block)))>0)check("scratch-copy-write",write(fd,block,n)==n);check("scratch-copy-read",n==0);close(in);close(fd);
  char *args[]={"/scratch/program","spawn-control",NULL};execv(args[0],args);check("scratch-noexec",errno==EPERM||errno==EACCES);
 }
 if(!strcmp(mode,"denials")){probes(1,argv[0]);}
 if(!strcmp(mode,"observables")){
  check("outside-metadata-observable",stat("/etc/os-release",&st)==0);
  fd=open("/etc/os-release",O_RDONLY);check("outside-content-denial",fd<0&&(errno==EPERM||errno==EACCES));
  struct timespec a,b;check("clock",clock_gettime(CLOCK_MONOTONIC,&a)==0);usleep(1000);check("advancing-clock",clock_gettime(CLOCK_MONOTONIC,&b)==0&&(b.tv_sec>a.tv_sec||(b.tv_sec==a.tv_sec&&b.tv_nsec>a.tv_nsec)));
  unsigned char entropy[32];check("entropy",syscall(SYS_getrandom,entropy,sizeof(entropy),0)==sizeof(entropy));
 }
 if(!strcmp(mode,"ignored-denial")){fd=socket(AF_UNIX,SOCK_STREAM,0);check("ignored-denial-control",fd<0&&errno==EPERM);}
 if(!strcmp(mode,"self-reexec")){char *args[]={"changed","changed-launch",NULL};char *env[]={"CHANGED=fixture",NULL};execve("/input/program",args,env);check("self-reexec",0);}
 if(!strncmp(mode,"stdout-",7)||!strncmp(mode,"stderr-",7)){int stream=mode[3]=='o'?1:2;emit(stream,33554432u+(strstr(mode,"overflow")?1u:0u),stream==1?'x':'y');return 0;}
 if(!strcmp(mode,"hold")||!strcmp(mode,"wait")){puts("static-ready");fflush(stdout);sleep(!strcmp(mode,"hold")?5:45);puts("static-done");return 0;}
 if(!strcmp(mode,"failure")){puts("static-failure");return 9;}
 puts("static-positive");return 0;
}
`

type staticBoundaryFixture struct {
	backend         Backend
	program, docker string
	request         EnvelopeRequest
}

func prepareStaticBoundaryFixture(t *testing.T, ctx context.Context) staticBoundaryFixture {
	t.Helper()
	image, arch := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_NATIVE_ARCH")
	if image == "" {
		t.Skip("installed immutable Linux GCC image required for independent static C probes")
	}
	if arch == "" {
		arch = "arm64"
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
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
	f := staticBoundaryFixture{backend: Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}, docker: docker, program: filepath.Join(dir, "static-program")}
	if err := f.backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.c"), []byte(independentStaticProgram), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--mount", "type=bind,source="+dir+",target=/build", "--entrypoint=/usr/bin/gcc", image, "-static", "-O2", "-Wl,-z,noexecstack", "-o", "/build/static-program", "/build/probe.c")
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("independent C static build failed: %v %s", err, out.Stderr)
	}
	f.request, _ = envelopeFixture(t)
	f.request.Env = []string{"DECLARED=fixture", "FIXED=second"}
	f.request.ScratchBytes = 8 << 20
	return f
}

func (f staticBoundaryFixture) capture(t *testing.T, mode string) (*Program, *Envelope) {
	t.Helper()
	args := []string{mode, "argument with spaces"}
	p, err := CaptureProgram(context.Background(), ProgramRequest{Program: f.program, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "caller mutation must not retarget"
	return p, captureFixtureEnvelope(t, f.request)
}

func TestInstalledLinuxStaticProgramIndependentBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	out, err := command(ctx, f.docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--tmpfs", "/tmp:rw,mode=1777", "--mount", "type=bind,source="+f.program+",target=/control,readonly", "--entrypoint=/control", f.backend.Image, "outside-control")
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "outside-static-controls-pass\n" || len(out.Stderr) != 0 {
		t.Fatalf("independent outside controls unavailable: %v %s", err, out.Stderr)
	}
	t.Run("startup-fd-stress", func(t *testing.T) {
		p, e := f.capture(t, "positive")
		for i := 0; i < 16; i++ {
			out, err := RunProgram(ctx, f.backend, p, e)
			if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "static-positive\n" || len(out.Stderr) != 0 {
				t.Fatalf("healthy strictFD startup %d failed: %v %s", i, err, out.Stderr)
			}
			assertNoManagedContainers(t, f.docker)
		}
	})
	for _, mode := range []string{"positive", "positive", "scratch-limits", "denials", "observables", "ignored-denial", "self-reexec", "failure"} {
		t.Run(mode, func(t *testing.T) {
			p, e := f.capture(t, mode)
			out, err := RunProgram(ctx, f.backend, p, e)
			marker, exit := "static-positive\n", 0
			if mode == "self-reexec" {
				marker = "static-self-reexec-changed\n"
			}
			if mode == "failure" {
				marker, exit = "static-failure\n", 9
			}
			if !out.Completed || out.ExitCode != exit || string(out.Stdout) != marker || len(out.Stderr) != 0 || (exit == 0 && err != nil) || (exit != 0 && err == nil) {
				t.Fatalf("static mode %s: complete=%t exit=%d error=%v stderr=%s", mode, out.Completed, out.ExitCode, err, out.Stderr)
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.program, e)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxStaticProgramIndependentOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	for _, mode := range []string{"stdout-maximum", "stderr-maximum", "stdout-overflow", "stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			p, e := f.capture(t, mode)
			out, err := RunProgram(ctx, f.backend, p, e)
			overflow := strings.HasSuffix(mode, "overflow")
			// Framing must never leak into returned guest bytes, including on
			// rejected overflow; inspect the raw output without test-side repair.
			data, other, value := out.Stdout, out.Stderr, "x"
			if strings.HasPrefix(mode, "stderr") {
				data, other, value = out.Stderr, out.Stdout, "y"
			}
			if len(data) > 32<<20 || len(other) != 0 || strings.Trim(string(data), value) != "" {
				t.Fatalf("independent stream control failed: completed=%t payload=%d other=%d error=%v", out.Completed, len(data), len(other), err)
			}
			if overflow {
				if err == nil || out.Completed {
					t.Fatal("actual output overflow admitted")
				}
			} else if err != nil || !out.Completed || out.ExitCode != 0 || len(data) != 32<<20 {
				t.Fatalf("maximum output control failed: %v len=%d", err, len(data))
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

type staticProgramResult struct {
	out process.Output
	err error
}

func waitStaticProgramReady(t *testing.T, ctx context.Context, f staticBoundaryFixture, done <-chan staticProgramResult) string {
	t.Helper()
	for {
		select {
		case <-done:
			t.Fatal("static program exited before actual READY")
		case <-ctx.Done():
			t.Fatal("static program readiness exceeded bound")
		default:
		}
		ids, err := command(ctx, f.docker, "ps", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range strings.Fields(string(ids.Stdout)) {
			logs, err := command(ctx, f.docker, "logs", id)
			if err == nil && strings.Contains(string(logs.Stdout), "static-ready\n") {
				return id
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func staticProgramCopyRoot(t *testing.T, ctx context.Context, f staticBoundaryFixture, id string) string {
	t.Helper()
	out, err := command(ctx, f.docker, "inspect", id, "--format", "{{json .Mounts}}")
	var mounts []struct{ Source, Destination string }
	if err != nil || json.Unmarshal(out.Stdout, &mounts) != nil {
		t.Fatal("actual copied input mount unavailable")
	}
	for _, m := range mounts {
		if m.Destination == "/input" {
			return m.Source
		}
	}
	t.Fatal("actual private input mount absent")
	return ""
}

func TestInstalledLinuxStaticProgramIndependentLiveGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	for _, fault := range []string{"healthy", "source-bytes", "source-mode", "source-inode", "copy-mode", "copy-inode", "cleanup"} {
		t.Run(fault, func(t *testing.T) {
			original, err := os.ReadFile(f.program)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(f.program)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(f.program, original, info.Mode()); err != nil {
					t.Error(err)
				}
				if err := os.Chmod(f.program, info.Mode()); err != nil {
					t.Error(err)
				}
			})
			p, e := f.capture(t, "hold")
			bounded, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			done := make(chan staticProgramResult, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				out, err := RunProgram(bounded, f.backend, p, e)
				done <- staticProgramResult{out, err}
			}()
			defer func() { stop(); <-joined }()
			id := waitStaticProgramReady(t, bounded, f, done)
			root := staticProgramCopyRoot(t, bounded, f, id)
			t.Cleanup(func() { _ = os.Chmod(root, 0755); _ = os.RemoveAll(root) })
			expected := ""
			switch fault {
			case "source-bytes":
				expected = "native static executable source changed"
				err = os.WriteFile(f.program, append(original, 0), info.Mode())
			case "source-mode":
				expected = "native static executable source changed"
				err = os.Chmod(f.program, 0600)
			case "source-inode":
				expected = "native static executable source changed"
				parent, statErr := os.Stat(filepath.Dir(f.program))
				if statErr != nil {
					t.Fatal(statErr)
				}
				replacement := filepath.Join(t.TempDir(), "replacement")
				if err = os.WriteFile(replacement, original, info.Mode()); err == nil {
					err = os.Chtimes(replacement, info.ModTime(), info.ModTime())
				}
				if err == nil {
					err = os.Rename(replacement, f.program)
				}
				if err == nil {
					err = os.Chtimes(filepath.Dir(f.program), parent.ModTime(), parent.ModTime())
				}
				after, statErr := os.Stat(f.program)
				if statErr != nil || os.SameFile(info, after) || info.Size() != after.Size() || info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) {
					t.Fatal("source replacement did not preserve bytes/metadata with distinct inode")
				}
			case "copy-mode":
				expected = "native static executable copy changed"
				err = os.Chmod(filepath.Join(root, "program"), 0755)
			case "copy-inode":
				expected = "native static executable copy changed"
				copy := filepath.Join(root, "program")
				before, statErr := os.Stat(copy)
				if statErr != nil {
					t.Fatal(statErr)
				}
				replacement := filepath.Join(root, "replacement")
				if err = os.WriteFile(replacement, original, 0555); err == nil {
					err = os.Chtimes(replacement, before.ModTime(), before.ModTime())
				}
				if err == nil {
					err = os.Rename(replacement, copy)
				}
				after, statErr := os.Stat(copy)
				if statErr != nil || os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("distinct copied inode with identical bytes/metadata not established")
				}
			case "cleanup":
				expected = "native copied input cleanup failed"
				err = os.Chmod(root, 0500)
			}
			if err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.out.ExitCode != 0 || string(result.out.Stdout) != "static-ready\nstatic-done\n" || len(result.out.Stderr) != 0 {
				t.Fatalf("fault masked actual successful program: %+v %v", result.out, result.err)
			}
			if fault == "healthy" {
				if result.err != nil || !result.out.Completed {
					t.Fatal("healthy postguard failed", result.err)
				}
			} else if result.err == nil || result.out.Completed || !strings.Contains(result.err.Error(), expected) {
				t.Fatalf("%s concealed after exit0: %v", fault, result.err)
			}
			if fault == "cleanup" {
				if _, err := os.Stat(root); err != nil {
					t.Fatal("cleanup failure remnant control absent")
				}
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("private copied input directory remained")
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.program, e)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxStaticProgramIndependentDeadlineAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	for _, kind := range []string{"cancel", "fixed-deadline"} {
		t.Run(kind, func(t *testing.T) {
			p, e := f.capture(t, "wait")
			bounded, stop := context.WithTimeout(ctx, 45*time.Second)
			defer stop()
			done := make(chan staticProgramResult, 1)
			joined := make(chan struct{})
			started := time.Now()
			go func() {
				defer close(joined)
				out, err := RunProgram(bounded, f.backend, p, e)
				done <- staticProgramResult{out, err}
			}()
			defer func() { stop(); <-joined }()
			id := waitStaticProgramReady(t, bounded, f, done)
			root := staticProgramCopyRoot(t, bounded, f, id)
			if kind == "cancel" {
				stop()
			}
			result := <-done
			elapsed := time.Since(started)
			if result.err == nil || result.out.Completed || !strings.Contains(string(result.out.Stdout), "static-ready\n") || strings.Contains(string(result.out.Stdout), "static-done") {
				t.Fatalf("active %s falsely completed: %v", kind, result.err)
			}
			cause := context.Canceled
			if kind == "fixed-deadline" {
				cause = context.DeadlineExceeded
			}
			if !errors.Is(result.err, cause) {
				t.Fatalf("active %s lost typed context cause: %v", kind, result.err)
			}
			if kind == "fixed-deadline" && (elapsed < 28*time.Second || elapsed > 40*time.Second) {
				t.Fatalf("fixed30s policy deadline not observed: %s", elapsed)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("canceled private input directory remained")
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxStaticProgramIndependentAliasRetarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	alias := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(filepath.Dir(filepath.Dir(f.program)), alias); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(alias, filepath.Base(filepath.Dir(f.program)), filepath.Base(f.program))
	p, err := CaptureProgram(ctx, ProgramRequest{Program: selected, Args: []string{"positive", "argument with spaces"}})
	if err != nil {
		t.Fatal("healthy actual ancestor alias capture", err)
	}
	e := captureFixtureEnvelope(t, f.request)
	out, err := RunProgram(ctx, f.backend, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "static-positive\n" {
		t.Fatal("healthy actual ancestor alias execution", err)
	}
	data, err := os.ReadFile(f.program)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.program)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	otherDir := filepath.Join(other, filepath.Base(filepath.Dir(f.program)))
	if err := os.Mkdir(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	otherProgram := filepath.Join(otherDir, filepath.Base(f.program))
	if err := os.WriteFile(otherProgram, data, info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(otherProgram, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(otherProgram)
	if err != nil || os.SameFile(info, otherInfo) || info.Size() != otherInfo.Size() || info.Mode() != otherInfo.Mode() || !info.ModTime().Equal(otherInfo.ModTime()) {
		t.Fatal("equal executable bytes/metadata at distinct alias target not established")
	}
	p, err = CaptureProgram(ctx, ProgramRequest{Program: selected, Args: []string{"hold", "argument with spaces"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan staticProgramResult, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		out, err := RunProgram(ctx, f.backend, p, e)
		done <- staticProgramResult{out, err}
	}()
	defer func() { cancel(); <-joined }()
	id := waitStaticProgramReady(t, ctx, f, done)
	root := staticProgramCopyRoot(t, ctx, f, id)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err == nil || !strings.Contains(result.err.Error(), "native static executable source changed") || result.out.Completed || result.out.ExitCode != 0 || string(result.out.Stdout) != "static-ready\nstatic-done\n" || len(result.out.Stderr) != 0 {
		t.Fatalf("same-byte source alias retarget concealed by successful copied execution: %v", result.err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("alias-retarget execution directory remained")
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.program, e)
	assertNoManagedContainers(t, f.docker)
}

func TestInstalledLinuxStaticProgramIndependentAggregateBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareStaticBoundaryFixture(t, ctx)
	p, healthy := f.capture(t, "positive")
	out, err := RunProgram(ctx, f.backend, p, healthy)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "static-positive\n" {
		t.Fatal("healthy aggregate baseline failed", err)
	}
	root := t.TempDir()
	// Sparse files avoid writing half a GiB of fixture data to disk. Capture
	// must still inspect and retain the actual bounded bytes; no forged bundle
	// or production hook manufactures an admitted manifest.
	for i := 0; i < 8; i++ {
		name := filepath.Join(root, string(rune('a'+i)))
		file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		truncateErr := file.Truncate(64 << 20)
		closeErr := file.Close()
		if truncateErr != nil || closeErr != nil {
			t.Fatal("actual sparse resource creation failed", truncateErr, closeErr)
		}
		info, err := os.Stat(name)
		if err != nil || info.Size() != 64<<20 || !info.Mode().IsRegular() {
			t.Fatal("actual sparse size control failed")
		}
	}
	request := f.request
	request.Files = inputbundle.Request{Roots: []inputbundle.Root{{Name: "aggregate", Path: root, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "aggregate", Path: "."}}}
	request.Absent = nil
	e, err := CaptureEnvelope(ctx, request)
	if err != nil {
		t.Fatal("exact512MiB envelope capture failed", err)
	}
	if e.files.Manifest().Bytes != 512<<20 || p.executable.files.Manifest().Bytes <= 0 {
		t.Fatal("independent source sizes did not establish aggregate overflow")
	}
	out, err = RunProgram(ctx, f.backend, p, e)
	if err == nil || !strings.Contains(err.Error(), "native program inputs exceed aggregate byte bounds") || out.Completed || len(out.Stdout) != 0 || len(out.Stderr) != 0 {
		t.Fatalf("individually bounded inputs exceeded total policy unnoticed: %v", err)
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.program, e)
	assertNoManagedContainers(t, f.docker)
}

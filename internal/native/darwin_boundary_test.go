package native

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

// This independent Mach-O probe tests operational containment. Its observable
// loader, clock and entropy inputs deliberately do not establish a provider.
const darwinIndependentProbe = `
#include <arpa/inet.h>
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>
extern char **environ;
static int denied(int e){return e==EPERM || e==EACCES;}
static int finish(int ok,const char *name){if(!ok)return 125;printf("%s_OK\n",name);return 0;}
int main(int argc,char **argv){
 if(argc<2)return 125;
 const char *mode=argv[1];
 if(!strcmp(mode,"state")){
  char cwd[4096],byte;int n=0;while(environ[n])n++;
  if(argc!=4 || strcmp(argv[2],"literal-one") || strcmp(argv[3],"literal-two")){fputs("STATE_ARGV_FAIL\n",stderr);return 125;}
  if(n!=2 || !getenv("FIRST") || !getenv("SECOND") || strcmp(getenv("FIRST"),"fixture") || strcmp(getenv("SECOND"),"control")){fputs("STATE_ENV_FAIL\n",stderr);return 125;}
  if(read(0,&byte,1)!=0){fprintf(stderr,"STATE_STDIN_FAIL:%d\n",errno);return 125;}
  struct stat here,declared;char parent[4096];if(strlen(argv[0])>=sizeof(parent))return 125;strcpy(parent,argv[0]);char *slash=strrchr(parent,'/');if(!slash)return 125;*slash=0;
  if(stat(".",&here) || stat(parent,&declared) || here.st_dev!=declared.st_dev || here.st_ino!=declared.st_ino){fputs("STATE_CWD_ID_FAIL\n",stderr);return 125;}
  int cwdAvailable=getcwd(cwd,sizeof(cwd))!=NULL;if(!cwdAvailable&&!denied(errno)){fprintf(stderr,"STATE_CWD_FAIL:%d\n",errno);return 125;}
  for(int fd=3;fd<32;fd++){errno=0;if(fcntl(fd,F_GETFD)!=-1 || errno!=EBADF){fprintf(stderr,"STATE_FD_FAIL:%d:%d\n",fd,errno);return 125;}}
  puts("STATE_OK");puts("CWD_ID_OK");if(cwdAvailable)printf("CWD_AVAILABLE=%s\n",cwd);else puts("CWD_PERMISSION_DENIED");return 0;
 }
 if(!strcmp(mode,"observe")){
  unsigned char entropy[32]={0},header[4];struct timespec a,b;unsigned int nonzero=0;
  int fd=open("/usr/lib/dyld",O_RDONLY);if(fd<0 || read(fd,header,4)!=4)return 125;close(fd);
  struct stat st;if(stat("/usr/lib/dyld",&st) || st.st_size<4 || clock_gettime(CLOCK_MONOTONIC,&a))return 125;
  usleep(1000);if(clock_gettime(CLOCK_MONOTONIC,&b) || (a.tv_sec==b.tv_sec && a.tv_nsec==b.tv_nsec))return 125;
  arc4random_buf(entropy,sizeof(entropy));for(int i=0;i<32;i++)nonzero|=entropy[i];
  return finish(nonzero!=0,"OBSERVABLE_LOADER_CLOCK_ENTROPY");
 }
 if(!strcmp(mode,"stopped")){puts("READY");fflush(stdout);raise(SIGSTOP);puts("DONE");return 0;}
 if(!strcmp(mode,"tree")){
  pid_t child=fork();if(child<0)return 125;if(child==0){for(;;)pause();}
  puts("TREE_READY");fflush(stdout);raise(SIGSTOP);kill(child,SIGKILL);waitpid(child,0,0);return 0;
 }
 if(!strcmp(mode,"output")){
  if(argc!=3)return 125;size_t left=strtoull(argv[2],0,10);char block[8192];memset(block,'X',sizeof(block));
  while(left){size_t n=left<sizeof(block)?left:sizeof(block);ssize_t wrote=write(1,block,n);if(wrote<0)return 125;left-=wrote;}return 0;
 }
 if(argc!=4)return 125;
 int wantDenied=!strcmp(argv[3],"deny"),ok=0;
 if(!strcmp(mode,"read")){char value[64];int fd=open(argv[2],O_RDONLY);int e=errno;if(fd<0)ok=wantDenied&&denied(e);else{ssize_t n=read(fd,value,sizeof(value));close(fd);ok=!wantDenied&&n>0;}}
 else if(!strcmp(mode,"metadata")){struct stat st;int r=stat(argv[2],&st),e=errno;ok=wantDenied?(r<0&&denied(e)):(r==0&&st.st_size>0);}
 else if(!strcmp(mode,"directory")){DIR *d=opendir(argv[2]);int e=errno;ok=wantDenied?(!d&&denied(e)):(d!=NULL);if(d)closedir(d);}
 else if(!strcmp(mode,"write")){int fd=open(argv[2],O_WRONLY|O_TRUNC),e=errno;if(fd<0)ok=wantDenied&&denied(e);else{ok=!wantDenied&&write(fd,"OUTSIDE_WRITE_CONTROL\n",22)==22;close(fd);}}
 else if(!strcmp(mode,"fork")){pid_t pid=fork();int e=errno;if(pid<0)ok=wantDenied&&denied(e);else if(pid==0){_exit(0);}else{int status;ok=!wantDenied&&waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0;}}
 else if(!strcmp(mode,"exec")){execl("/usr/bin/true","true",NULL);ok=wantDenied&&denied(errno);}
 else if(!strcmp(mode,"tcp") || !strcmp(mode,"unix")){
  int family=!strcmp(mode,"tcp")?AF_INET:AF_UNIX;int fd=socket(family,SOCK_STREAM,0);if(fd<0)return 125;int r;
  if(family==AF_INET){struct sockaddr_in a={0};a.sin_family=AF_INET;a.sin_port=htons(atoi(argv[2]));inet_pton(AF_INET,"127.0.0.1",&a.sin_addr);r=connect(fd,(struct sockaddr*)&a,sizeof(a));}
  else{struct sockaddr_un a={0};a.sun_family=AF_UNIX;if(strlen(argv[2])>=sizeof(a.sun_path))return 125;strcpy(a.sun_path,argv[2]);r=connect(fd,(struct sockaddr*)&a,sizeof(a));}
  int e=errno;if(r<0)ok=wantDenied&&denied(e);else{char marker;ok=!wantDenied&&read(fd,&marker,1)==1&&marker=='P';}close(fd);
 }
 return finish(ok,mode);
}`

func darwinBoundaryProgram(t *testing.T) (Backend, string) {
	t.Helper()
	backend, dir := darwinFixtureBackend(t)
	source, program := filepath.Join(dir, "independent.c"), filepath.Join(dir, "independent-probe")
	if err := os.WriteFile(source, []byte(darwinIndependentProbe), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := process.RunEnvironment(ctx, model.Command{Executable: "/usr/bin/clang", Args: []string{"-x", "c", "-O0", "-Wall", "-o", program, source}}, dir, []string{"PATH=/usr/bin:/bin"})
	if err != nil || !output.Completed || output.ExitCode != 0 {
		t.Fatal("installed compiler failed independent Mach-O fixture", err)
	}
	program, err = filepath.EvalSymlinks(program)
	if err != nil {
		t.Fatal(err)
	}
	return backend, program
}

func darwinBoundaryRun(t *testing.T, b Backend, program string, args, env []string, outside bool) process.Output {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output process.Output
	var err error
	if outside {
		output, err = process.RunEnvironment(ctx, model.Command{Executable: program, Args: args}, filepath.Dir(program), env)
	} else {
		output, err = b.execute(ctx, Artifact{Program: program, Args: args, Env: env})
	}
	if err != nil || !output.Completed || output.ExitCode != 0 || len(output.Stderr) != 0 {
		t.Fatalf("independent Darwin operational %s control failed: completed=%t exit=%d error=%v fixture-diagnostic=%q", args[0], output.Completed, output.ExitCode, err, output.Stderr)
	}
	return output
}

func darwinReachableServer(t *testing.T, network, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = connection.Write([]byte{'P'})
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	return listener
}

func TestInstalledDarwinIndependentOperationalBoundary(t *testing.T) {
	b, program := darwinBoundaryProgram(t)
	control := filepath.Join(t.TempDir(), "existing-control")
	if err := os.WriteFile(control, []byte("BOUNDARY_RESOURCE_CONTROL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tcp := darwinReachableServer(t, "tcp4", "127.0.0.1:0")
	unixDir, err := os.MkdirTemp("/private/tmp", "hayaku-darwin-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(unixDir) })
	unix := darwinReachableServer(t, "unix", filepath.Join(unixDir, "socket"))
	_, port, err := net.SplitHostPort(tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mode, target string }{
		{"read", control}, {"metadata", control}, {"directory", filepath.Dir(control)},
		{"write", control}, {"fork", "unused"}, {"exec", "unused"},
		{"tcp", port}, {"unix", unix.Addr().String()},
	} {
		t.Run(c.mode, func(t *testing.T) {
			outside := darwinBoundaryRun(t, b, program, []string{c.mode, c.target, "allow"}, nil, true)
			if c.mode != "exec" && string(outside.Stdout) != c.mode+"_OK\n" {
				t.Fatal("known successful outside control missing")
			}
			before, err := os.ReadFile(control)
			if err != nil {
				t.Fatal(err)
			}
			inside := darwinBoundaryRun(t, b, program, []string{c.mode, c.target, "deny"}, nil, false)
			if string(inside.Stdout) != c.mode+"_OK\n" {
				t.Fatal("actual permission denial missing")
			}
			after, err := os.ReadFile(control)
			if err != nil || !slices.Equal(before, after) {
				t.Fatal("contained program changed outside control")
			}
		})
	}
	for _, outside := range []bool{true, false} {
		state := darwinBoundaryRun(t, b, program, []string{"state", "literal-one", "literal-two"}, []string{"FIRST=fixture", "SECOND=control"}, outside)
		lines := strings.Split(strings.TrimSpace(string(state.Stdout)), "\n")
		if len(lines) != 3 || lines[0] != "STATE_OK" || lines[1] != "CWD_ID_OK" {
			t.Fatal("exact initial argv/env/stdin/fd control missing")
		}
		if outside {
			if lines[2] != "CWD_AVAILABLE="+filepath.Dir(program) {
				t.Fatal("known outside cwd path control unavailable")
			}
		} else if lines[2] != "CWD_PERMISSION_DENIED" {
			// libc getcwd may need ancestor directory reads; matching inode/dev
			// establishes initial cwd without claiming ordinary-cwd equivalence.
			t.Fatal("unknown cwd observability limitation")
		}
		observed := darwinBoundaryRun(t, b, program, []string{"observe"}, nil, outside)
		if string(observed.Stdout) != "OBSERVABLE_LOADER_CLOCK_ENTROPY_OK\n" {
			t.Fatal("trusted loader/clock/entropy limitations differ")
		}
	}
}

func darwinExecutionDirectories(t *testing.T) map[string]bool {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(os.TempDir(), "hayaku-darwin-execution-*"))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]bool{}
	for _, name := range names {
		canonical, err := filepath.EvalSymlinks(name)
		if err != nil {
			t.Fatal(err)
		}
		result[canonical] = true
	}
	return result
}

// SIGSTOP occurs after READY was flushed, so faults cannot be mistaken for a
// startup failure or a merely scheduled-but-not-running program.
func darwinStoppedCopy(t *testing.T, ctx context.Context, before map[string]bool) (string, int) {
	t.Helper()
	for ctx.Err() == nil {
		for dir := range darwinExecutionDirectories(t) {
			if before[dir] {
				continue
			}
			program := filepath.Join(dir, "program")
			output, err := process.RunEnvironment(ctx, model.Command{Executable: "/bin/ps", Args: []string{"-A", "-o", "pid=", "-o", "state=", "-o", "comm="}}, "", []string{})
			if err != nil || !output.Completed {
				t.Fatal("controlled stopped-process observation failed", err)
			}
			for _, line := range strings.Split(string(output.Stdout), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && strings.Contains(fields[1], "T") && fields[2] == program {
					pid, err := strconv.Atoi(fields[0])
					if err != nil {
						t.Fatal(err)
					}
					return dir, pid
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("independent program did not reach its stopped ready checkpoint")
	return "", 0
}

func TestInstalledDarwinPostExecutionGuardsAndCancellation(t *testing.T) {
	b, program := darwinBoundaryProgram(t)
	original, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"healthy", "source-bytes", "copy-mode", "cleanup", "cancel", "deadline"} {
		t.Run(fault, func(t *testing.T) {
			before := darwinExecutionDirectories(t)
			limit := 10 * time.Second
			if fault == "deadline" {
				limit = 2 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			type answer struct {
				output process.Output
				err    error
			}
			done := make(chan answer, 1)
			go func() {
				output, err := b.execute(ctx, Artifact{Program: program, Args: []string{"stopped"}})
				done <- answer{output, err}
			}()
			joined := false
			defer func() {
				cancel()
				if !joined {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("controlled Darwin process did not join")
					}
				}
			}()
			dir, pid := darwinStoppedCopy(t, ctx, before)
			defer func() {
				if !joined {
					_ = syscall.Kill(pid, syscall.SIGCONT)
				}
				_ = os.Chmod(dir, 0700)
				_ = os.RemoveAll(dir)
				_ = os.WriteFile(program, original, 0700)
			}()
			switch fault {
			case "source-bytes":
				if err := os.WriteFile(program, append(slices.Clone(original), 0), 0700); err != nil {
					t.Fatal(err)
				}
			case "copy-mode":
				if err := os.Chmod(filepath.Join(dir, "program"), 0755); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				if err := os.Chmod(dir, 0555); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "cancel" {
				cancel()
			} else if fault != "deadline" {
				if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
			}
			var result answer
			select {
			case result = <-done:
				joined = true
			case <-time.After(3 * time.Second):
				t.Fatal("controlled Darwin execution did not join")
			}
			if fault == "cancel" || fault == "deadline" {
				if result.err == nil || result.output.Completed || !strings.Contains(string(result.output.Stdout), "READY\n") {
					t.Fatal("active cancellation returned complete execution")
				}
				if fault == "deadline" && !errors.Is(result.err, context.DeadlineExceeded) {
					t.Fatal("active native deadline was not enforced", result.err)
				}
			} else {
				if result.output.ExitCode != 0 || string(result.output.Stdout) != "READY\nDONE\n" || len(result.output.Stderr) != 0 {
					t.Fatal("postguard fault was masked by program failure")
				}
				want := "native macOS program changed during execution"
				if fault == "cleanup" {
					want = "native macOS cleanup failed"
				}
				if fault == "healthy" {
					if result.err != nil || !result.output.Completed {
						t.Fatal("healthy stopped control failed", result.err)
					}
				} else if result.err == nil || result.output.Completed || !strings.Contains(result.err.Error(), want) {
					t.Fatal("actual post-execution fault returned complete execution", result.err)
				}
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatal("controlled Darwin process remains alive after join")
			}
			if fault != "cleanup" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("owned private execution directory remains after join")
				}
			}
		})
	}
}

func TestInstalledDarwinOutputBoundary(t *testing.T) {
	b, program := darwinBoundaryProgram(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, size := range []int{process.MaxOutput, process.MaxOutput + 1} {
		before := darwinExecutionDirectories(t)
		output, err := b.execute(ctx, Artifact{Program: program, Args: []string{"output", strconv.Itoa(size)}})
		if size == process.MaxOutput {
			if err != nil || !output.Completed || output.ExitCode != 0 || len(output.Stdout) != size || len(output.Stderr) != 0 {
				t.Fatal("healthy exact output bound failed", err)
			}
		} else if err == nil || output.Completed || len(output.Stdout) > process.MaxOutput {
			t.Fatal("overflow returned complete or unbounded execution")
		}
		if !reflect.DeepEqual(before, darwinExecutionDirectories(t)) {
			t.Fatal("output execution left private copied inputs")
		}
	}
}

// The known-positive outside fork creates an actual descendant for the common
// macOS process-group cleanup oracle. The inside probe separately denies fork;
// this does not claim that arbitrary descendants cannot leave a process group.
func TestInstalledDarwinOriginalProcessTreeCancellation(t *testing.T) {
	_, program := darwinBoundaryProgram(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type answer struct {
		output process.Output
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		output, err := process.RunEnvironment(ctx, model.Command{Executable: program, Args: []string{"tree"}}, filepath.Dir(program), nil)
		done <- answer{output, err}
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("controlled original process tree did not join")
			}
		}
	}()
	var pids []int
	for ctx.Err() == nil {
		output, err := process.RunEnvironment(ctx, model.Command{Executable: "/bin/ps", Args: []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "state=", "-o", "comm="}}, "", nil)
		if err != nil || !output.Completed {
			t.Fatal("controlled process-tree observation failed", err)
		}
		pids = nil
		parent, group, stopped := 0, 0, false
		for _, line := range strings.Split(string(output.Stdout), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[3] != program {
				continue
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				t.Fatal(err)
			}
			pgid, err := strconv.Atoi(fields[1])
			if err != nil || group != 0 && group != pgid {
				t.Fatal("controlled descendant left its expected process group", err)
			}
			group = pgid
			pids = append(pids, pid)
			if strings.Contains(fields[2], "T") {
				parent, stopped = pid, true
			}
		}
		if len(pids) == 2 && stopped && parent == group {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("actual original descendant and stopped parent control missing")
	}
	cancel()
	select {
	case result := <-done:
		joined = true
		if !errors.Is(result.err, context.Canceled) || result.output.Completed || string(result.output.Stdout) != "TREE_READY\n" {
			t.Fatal("original process-tree cancellation returned complete output")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original process-tree cancellation did not join")
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, pid := range pids {
		for time.Now().Before(deadline) && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			time.Sleep(5 * time.Millisecond)
		}
		if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			t.Fatal("actual original descendant remains alive after cancellation")
		}
	}
}

func darwinPureFixture(t *testing.T) (string, string, Config) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("actual macOS runtime required")
	}
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module darwinboundary\ngo 1.26\n")
	write(t, root, "value.go", "package sample;func Value()int{return 7}")
	write(t, root, "value_test.go", `package sample;import "testing";func TestAlpha(t *testing.T){if Value()!=7{t.Fatal("changed")}};func TestBeta(t *testing.T){if 2+2!=4{t.Fatal("control")}}`)
	return root, cache, Config{Schema: 1, Platform: "darwin", Architecture: runtime.GOARCH, Packages: []string{"."}}
}

func darwinPureOutcomes(t *testing.T, group Result, alpha string) {
	t.Helper()
	want := []model.Outcome{{Unit: ".", Test: "TestAlpha", Action: alpha}, {Unit: ".", Test: "TestBeta", Action: "pass"}}
	if !group.Qualified || !group.Complete || group.Context != "darwin-"+runtime.GOARCH || !reflect.DeepEqual(group.Outcomes.Tests, want) {
		t.Fatal("actual macOS inventory or independent terminal outcomes differ")
	}
}

func TestInstalledDarwinGoAuditAndTamperedReceipt(t *testing.T) {
	root, cache, config := darwinPureFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, step := range []string{"cold", "audit", "tamper"} {
		if step == "tamper" {
			entries, err := filepath.Glob(filepath.Join(cache, "*.json"))
			if err != nil || len(entries) != 1 {
				t.Fatal("healthy authenticated receipt missing", err)
			}
			if err := os.WriteFile(entries[0], []byte(`{"schema":1,"passed":true}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		report, err := Go(ctx, root, config, cache, step == "audit")
		if err != nil || !report.Passed || !report.Complete || len(report.Groups) != 1 || !report.Groups[0].Executed || report.Groups[0].Reused {
			t.Fatal("actual Darwin cold/audit/tampered receipt did not execute", step, err)
		}
		darwinPureOutcomes(t, report.Groups[0], "pass")
	}
}

func TestInstalledDarwinGoFailureCannotReuse(t *testing.T) {
	root, cache, config := darwinPureFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	healthy, err := Go(ctx, root, config, cache, false)
	if err != nil || !healthy.Complete || !healthy.Passed || len(healthy.Groups) != 1 {
		t.Fatal("healthy native receipt control failed", err)
	}
	darwinPureOutcomes(t, healthy.Groups[0], "pass")
	write(t, root, "value.go", "package sample;func Value()int{return 8}")
	for attempt := 0; attempt < 2; attempt++ {
		report, err := Go(ctx, root, config, cache, false)
		if err == nil || !report.Complete || report.Passed || len(report.Groups) != 1 || !report.Groups[0].Executed || report.Groups[0].Reused || report.Groups[0].Passed {
			t.Fatal("actual failing group was hidden or reused")
		}
		darwinPureOutcomes(t, report.Groups[0], "fail")
		if _, err := os.Stat(filepath.Join(cache, report.Groups[0].Key+".json")); !os.IsNotExist(err) {
			t.Fatal("failed native execution minted a reusable receipt")
		}
	}
}

func TestInstalledDarwinGoSkipCannotMintReceipt(t *testing.T) {
	root, cache, config := darwinPureFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	healthy, err := Go(ctx, root, config, cache, false)
	if err != nil || !healthy.Complete || !healthy.Passed || len(healthy.Groups) != 1 {
		t.Fatal("healthy native skip control unavailable", err)
	}
	darwinPureOutcomes(t, healthy.Groups[0], "pass")
	write(t, root, "optional_test.go", `package sample;import "testing";func TestOptional(t *testing.T){t.Skip("fixture")}`)
	for attempt := 0; attempt < 2; attempt++ {
		report, err := Go(ctx, root, config, cache, false)
		want := []model.Outcome{{Unit: ".", Test: "TestAlpha", Action: "pass"}, {Unit: ".", Test: "TestBeta", Action: "pass"}, {Unit: ".", Test: "TestOptional", Action: "skip"}}
		if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 || !report.Groups[0].Qualified || !report.Groups[0].Executed || report.Groups[0].Reused || !reflect.DeepEqual(report.Groups[0].Outcomes.Tests, want) {
			t.Fatal("actual native skip was hidden, lost or reused", err)
		}
		if _, err := os.Stat(filepath.Join(cache, report.Groups[0].Key+".json")); !os.IsNotExist(err) {
			t.Fatal("successful native skip minted an all-cases-pass receipt")
		}
	}
}

func TestInstalledDarwinGoAuthorityAndProducerDecoder(t *testing.T) {
	b, _ := darwinFixtureBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	build, err := captureGoBuildForPlatform(ctx, tool, "darwin", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	root, cache, _ := darwinPureFixture(t)
	source, temporary := t.TempDir(), t.TempDir()
	for _, name := range []string{"go.mod", "value.go", "value_test.go"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, source, name, string(data))
	}
	program := filepath.Join(temporary, "program")
	produce := func() Artifact {
		if _, err := runGo(ctx, tool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", program, "."); err != nil {
			t.Fatal(err)
		}
		files, err := packageSources(source)
		if err != nil {
			t.Fatal(err)
		}
		a := Artifact{Name: ".", Program: program, Proof: goSourceProof(files), Cases: []string{"TestAlpha", "TestBeta"}, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
		a, err = issueGoArtifact(ctx, b, a, build, root, source, source)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	decoder := func(process.Output, Artifact) (model.Outcomes, error) {
		t.Fatal("caller decoder acquired native receipt authority")
		return model.Outcomes{}, nil
	}
	a := produce()
	key, _, err := verifyAuthority(ctx, b, a)
	if err != nil || key == "" {
		t.Fatal("actual private Go producer admission unavailable", err)
	}
	healthy, err := Execute(ctx, b, a, cache, false, decoder)
	if err != nil || !healthy.Complete || !healthy.Passed || !healthy.Executed || healthy.Reused || healthy.Key != key {
		t.Fatal("actual reviewed decoder healthy control failed", err)
	}
	darwinPureOutcomes(t, healthy, "pass")
	before := observableCacheSnapshot(t, cache)
	workerBytes, err := os.ReadFile(b.Worker)
	if err != nil {
		t.Fatal(err)
	}
	retargeted := b
	retargeted.Worker = filepath.Join(temporary, "same-probe-other-location")
	if err := os.WriteFile(retargeted.Worker, workerBytes, 0700); err != nil {
		t.Fatal(err)
	}
	if err := retargeted.unchanged(ctx); err != nil {
		t.Fatal("same-byte role identity control failed before location fault", err)
	}
	for _, audit := range []bool{false, true} {
		for _, fault := range []string{"public", "json", "json-claim", "proof", "inventory", "environment", "clock", "metadata", "random", "unknown", "worker-location", "worker-bytes", "source-proof"} {
			t.Run(fault+map[bool]string{false: "/normal", true: "/audit"}[audit], func(t *testing.T) {
				artifact, backend := a, b
				artifact.Args, artifact.Env, artifact.Cases = slices.Clone(a.Args), slices.Clone(a.Env), slices.Clone(a.Cases)
				switch fault {
				case "public":
					artifact.authority = nil
				case "json", "json-claim":
					data, err := json.Marshal(a)
					if err != nil {
						t.Fatal(err)
					}
					if fault == "json-claim" {
						data = append(data[:len(data)-1], []byte(`,"qualified":true,"authority":{"binding":"forged"}}`)...)
					}
					artifact = Artifact{}
					if err := json.Unmarshal(data, &artifact); err != nil {
						t.Fatal(err)
					}
				case "proof":
					artifact.Proof = strings.Repeat("a", 64)
				case "inventory":
					artifact.Cases = []string{"TestAlpha", "TestAlpha"}
				case "environment":
					artifact.Env = append(artifact.Env, "UNDECLARED=fixture")
				case "clock", "metadata", "random", "unknown":
					effect := model.Effect{Owner: ".", Kind: fault}
					if fault == "metadata" {
						effect.Path = "existing-control"
					}
					artifact.Effects = []model.Effect{effect}
				case "worker-location":
					backend = retargeted
				case "worker-bytes":
					if err := os.WriteFile(b.Worker, append(slices.Clone(workerBytes), 0), 0700); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := os.WriteFile(b.Worker, workerBytes, 0700); err != nil {
							t.Error(err)
						}
					}()
				case "source-proof":
					write(t, source, "value.go", "package sample;func Value()int{return 8}")
					defer write(t, source, "value.go", "package sample;func Value()int{return 7}")
				}
				result, err := Execute(ctx, backend, artifact, cache, audit, decoder)
				if err == nil || result.Qualified || result.Complete || result.Passed || result.Executed || result.Reused || result.Key != "" {
					t.Fatal("forged or changed actual Darwin authority reached cache/audit")
				}
				if !reflect.DeepEqual(before, observableCacheSnapshot(t, cache)) {
					t.Fatal("rejected actual authority changed authenticated cache bytes")
				}
			})
		}
	}
	warm, err := Execute(ctx, b, a, cache, false, decoder)
	if err != nil || !warm.Reused || warm.Executed || !warm.Passed || warm.Key != key {
		t.Fatal("rejected faults revoked healthy prior native authority", err)
	}
	for _, dir := range []string{root, source} {
		write(t, dir, "value.go", "package sample;func Value()int{return 8}")
	}
	failing := produce()
	failed, err := Execute(ctx, b, failing, cache, false, decoder)
	if err == nil || !failed.Complete || failed.Passed || !failed.Executed || failed.Reused {
		t.Fatal("caller decoder hid actual native assertion failure")
	}
	darwinPureOutcomes(t, failed, "fail")
}

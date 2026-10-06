package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

// This independent probe compares actual outside syscall controls with the
// modified manual-loader boundary. It supplies no original-runtime proof.
const independentDynamicProgram = `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/shm.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <time.h>
extern char **environ;
static void check(const char *s,int ok){if(!ok){fprintf(stderr,"independent-dynamic-failed:%s errno=%d\n",s,errno);exit(71);}}
static void *thread(void *unused){int fd=open("data/value",O_RDONLY);char v[8];check("thread-data",fd>=0&&read(fd,v,8)==7&&!memcmp(v,"fixture",7));close(fd);fd=socket(AF_INET,SOCK_STREAM,0);check("thread-network",fd<0&&errno==EPERM);return NULL;}
static void *outside_thread(void *unused){return NULL;}
static void control(int denied,const char *self){
 int fd=socket(AF_INET,SOCK_STREAM,0);check("inet-socket",denied?fd<0&&errno==EPERM:fd>=0);if(fd>=0)close(fd);
 fd=socket(AF_UNIX,SOCK_STREAM,0);check("unix-socket",denied?fd<0&&errno==EPERM:fd>=0);if(fd>=0)close(fd);
 int pair[2];int rc=socketpair(AF_UNIX,SOCK_STREAM,0,pair);check("socketpair",denied?rc<0&&errno==EPERM:rc==0);if(rc==0){close(pair[0]);close(pair[1]);}
 void *rw=mmap(NULL,4096,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("ordinary-rw",rw!=MAP_FAILED);((char*)rw)[0]=3;
 rc=mprotect(rw,4096,PROT_READ|PROT_EXEC);check("rw-to-rx",denied?rc<0&&errno==EPERM:rc==0);munmap(rw,4096);
 void *rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("anonymous-rx",denied?rx==MAP_FAILED&&errno==EPERM:rx!=MAP_FAILED);if(rx!=MAP_FAILED)munmap(rx,4096);
 rx=mmap(NULL,4096,PROT_READ|PROT_WRITE|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("anonymous-rwx",denied?rx==MAP_FAILED&&errno==EPERM:rx!=MAP_FAILED);if(rx!=MAP_FAILED)munmap(rx,4096);
 fd=syscall(SYS_memfd_create,"independent-code",0);check("memfd",denied?fd<0&&errno==EPERM:fd>=0);if(fd>=0)close(fd);
 fd=open(denied?"/runtime/#program":self,O_RDONLY);check("known-executable-file-open",fd>=0);
 rx=(void*)syscall(SYS_mmap,NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,(1ULL<<32)|(unsigned)fd,0);check("file-rx-high-fd",denied?rx==MAP_FAILED&&errno==EPERM:rx!=MAP_FAILED);if(rx!=MAP_FAILED)munmap(rx,4096);
 if(denied){rx=(void*)syscall(SYS_mmap,NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|(1ULL<<32),fd,0);check("file-rx-high-flags",rx==MAP_FAILED&&errno==EPERM);rx=(void*)syscall(SYS_mmap,NULL,4096,PROT_READ|(1ULL<<32),MAP_PRIVATE,fd,0);check("file-high-prot",rx==MAP_FAILED&&errno==EPERM);}close(fd);

 int shmid=shmget(IPC_PRIVATE,4096,IPC_CREAT|0700);check("shmget-control",denied?shmid>=0||errno==EPERM:shmid>=0);
 if(shmid>=0){void *sh=shmat(shmid,NULL,0);check("shmat",denied?sh==(void*)-1&&errno==EPERM:sh!=(void*)-1);if(sh!=(void*)-1){((char*)sh)[0]=4;shmdt(sh);}check("shm-cleanup",shmctl(shmid,IPC_RMID,NULL)==0);}
 if(denied){void *sh=shmat(-1,NULL,SHM_EXEC);check("shmat-exec-denied",sh==(void*)-1&&errno==EPERM);}
 rc=pipe(pair);check("ordinary-pipe",rc==0);check("pipe-write",write(pair[1],"x",1)==1);int available=0;rc=ioctl(pair[0],FIONREAD,&available);check("ioctl",denied?rc<0&&errno==EPERM:rc==0&&available==1);int nonblock=1;rc=ioctl(pair[0],FIONBIO,&nonblock);check("foreign-fd-fionbio",denied?rc<0&&errno==EPERM:rc==0);close(pair[0]);close(pair[1]);
 for(int std=0;std<3;std++){int original=fcntl(std,F_GETFL);check("stdio-flags-positive",original>=0);nonblock=1;check("stdio-set-nonblock",ioctl(std,FIONBIO,&nonblock)==0&&(fcntl(std,F_GETFL)&O_NONBLOCK));nonblock=0;check("stdio-clear-nonblock",ioctl(std,FIONBIO,&nonblock)==0&&!(fcntl(std,F_GETFL)&O_NONBLOCK));if(original&O_NONBLOCK){nonblock=1;check("stdio-restore-original-flags",ioctl(std,FIONBIO,&nonblock)==0);}}
 nonblock=0;rc=syscall(SYS_ioctl,(1ULL<<32)|1,FIONBIO,&nonblock);check("ioctl-high-fd",denied?rc<0&&errno==EPERM:rc==0);rc=syscall(SYS_ioctl,1,(1ULL<<32)|FIONBIO,&nonblock);check("ioctl-high-request",denied?rc<0&&errno==EPERM:rc==0);rc=ioctl(1,FIONBIO,NULL);check("ioctl-pointee-kernel-check",rc<0&&errno==EFAULT);
 if(denied){int saved=dup(0);check("stdio-remap-saved-input",saved>=3);int resource=open("identical-code",O_RDONLY);check("stdio-remap-resource-open",resource>=3);struct stat before,after;check("stdio-remap-resource-stat",fstat(resource,&before)==0);check("stdio-remap",dup2(resource,0)==0);nonblock=1;check("stdio-remap-fionbio",ioctl(0,FIONBIO,&nonblock)==0&&(fcntl(0,F_GETFL)&O_NONBLOCK));check("stdio-remap-metadata",fstat(0,&after)==0&&before.st_dev==after.st_dev&&before.st_ino==after.st_ino&&before.st_mode==after.st_mode&&before.st_size==after.st_size&&before.st_mtim.tv_sec==after.st_mtim.tv_sec&&before.st_mtim.tv_nsec==after.st_mtim.tv_nsec);void *code=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,0,0);check("stdio-remap-still-noexec",code==MAP_FAILED&&(errno==EPERM||errno==EACCES));nonblock=0;check("stdio-remap-restore-flags",ioctl(0,FIONBIO,&nonblock)==0);check("stdio-restore-input",dup2(saved,0)==0);close(resource);close(saved);}

 const char *scratch=denied?"/scratch/permissions":"/tmp/permissions";fd=open(scratch,O_CREAT|O_RDWR,0600);check("permission-control-create",fd>=0);
 rc=fchmod(fd,0700);check("fchmod",denied?rc<0&&errno==EPERM:rc==0);
 rc=fchown(fd,getuid(),getgid());check("fchown",denied?rc<0&&errno==EPERM:rc==0);
 struct timespec times[2]={{123,0},{123,0}};rc=futimens(fd,times);check("futimens",denied?rc<0&&errno==EPERM:rc==0);close(fd);
 pid_t pid=fork();check("fork",denied?pid<0&&errno==EPERM:pid>=0);if(pid==0){char *args[]={(char*)self,"spawn",NULL};execv(self,args);_exit(72);}if(pid>0){int status;check("foreignexec-positive",waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0);}
 if(denied){char *args[]={"/boundary",NULL};execv(args[0],args);check("foreignexec-denial",errno==EPERM||errno==EACCES);
  errno=0;long result=syscall(SYS_clone,0x3d0f00ULL|(1ULL<<32),NULL,NULL,NULL,NULL);check("clone-high-flags",result<0&&errno==EPERM);}
fd=syscall(447,0);if(denied){check("memfd-secret-denial",fd<0&&errno==EPERM);}else{check("outside-secretmem-context",fd>=0||errno==ENOSYS||errno==EPERM||errno==EACCES);puts(fd>=0?"outside-secretmem-available":"outside-secretmem-system-unavailable");}if(fd>=0)close(fd);
#ifdef SYS_userfaultfd
 fd=syscall(SYS_userfaultfd,O_CLOEXEC|O_NONBLOCK);if(denied){check("userfaultfd-denial",fd<0&&errno==EPERM);}else{check("outside-userfaultfd-known-context",fd>=0||errno==EPERM||errno==EACCES);puts(fd>=0?"outside-userfaultfd-available":"outside-userfaultfd-system-denied");}if(fd>=0)close(fd);
#endif
}
static void emit(int fd,size_t n,char v){char block[8192];memset(block,v,sizeof(block));while(n){size_t amount=n<sizeof(block)?n:sizeof(block);ssize_t w=write(fd,block,amount);if(w<=0)_exit(74);n-=w;}}
int main(int argc,char **argv){
 if(argc==2&&!strcmp(argv[1],"spawn"))return 0;
 if(argc==2&&!strcmp(argv[1],"outside")){pthread_t t;check("outside-thread",pthread_create(&t,NULL,outside_thread,NULL)==0);check("outside-thread-join",pthread_join(t,NULL)==0);control(0,argv[0]);struct stat st;check("outside-stat",stat("/etc/os-release",&st)==0);int fd=open("/etc/os-release",O_RDONLY);check("outside-read",fd>=0);close(fd);puts("independent-dynamic-outside-pass");return 0;}
 check("argv",argc==3&&!strcmp(argv[0],"/runtime/#program")&&!strcmp(argv[2],"argument with spaces"));
 check("env",environ[0]&&environ[1]&&!environ[2]&&!strcmp(environ[0],"DECLARED=fixture")&&!strcmp(environ[1],"FIXED=second"));
 char cwd[1024],v[8];check("cwd",getcwd(cwd,sizeof(cwd))&&!strcmp(cwd,"/resources"));check("empty-stdin",read(0,v,1)==0);for(int fd=3;fd<32;fd++){errno=0;check("closed-extra-fd",fcntl(fd,F_GETFD)==-1&&errno==EBADF);}
 int fd=open("data/value",O_RDONLY);check("resource-data",fd>=0&&read(fd,v,8)==7&&!memcmp(v,"fixture",7));close(fd);struct stat st;check("resource-mode",stat("data/value",&st)==0&&(st.st_mode&0777)==0444);check("absence",stat("data/missing",&st)<0&&errno==ENOENT);
 check("fresh-scratch",stat("/scratch/state",&st)<0&&errno==ENOENT);fd=open("/scratch/state",O_CREAT|O_RDWR,0600);check("scratch-write",fd>=0&&write(fd,"x",1)==1);close(fd);
 const char *mode=argv[1];
 if(!strcmp(mode,"denials")){pthread_t t;check("inside-pthread",pthread_create(&t,NULL,thread,NULL)==0);check("inside-pthread-join",pthread_join(t,NULL)==0);control(1,argv[0]);
  fd=open("/runtime/#program",O_RDONLY);check("runtime-read",fd>=0);void *rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("runtime-rx",rx!=MAP_FAILED);check("runtime-cow-rw",mprotect(rx,4096,PROT_READ|PROT_WRITE)==0);((char*)rx)[0]^=1;check("runtime-cow-never-reexec",mprotect(rx,4096,PROT_READ|PROT_EXEC)<0&&errno==EPERM);munmap(rx,4096);close(fd);
  fd=open("identical-code",O_RDONLY);check("identical-elf-resource",fd>=0);rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("identical-resource-noexec",rx==MAP_FAILED&&(errno==EPERM||errno==EACCES));close(fd);
  fd=open("/scratch/code",O_CREAT|O_WRONLY,0700);check("scratch-code-create",fd>=0);char block[4096]={0};check("scratch-code-write",write(fd,block,sizeof(block))==sizeof(block));close(fd);fd=open("/scratch/code",O_RDONLY);rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("scratch-noexec",rx==MAP_FAILED&&(errno==EPERM||errno==EACCES));close(fd);
  fd=open("/etc/os-release",O_RDONLY);check("outside-content",fd<0&&(errno==EPERM||errno==EACCES));fd=open("/input/contract.json",O_RDONLY);check("control-content",fd<0&&(errno==EPERM||errno==EACCES));
 }
 if(!strcmp(mode,"observables")){struct timespec a,b;check("outside-metadata-observable",stat("/etc/os-release",&st)==0);check("clock",clock_gettime(CLOCK_MONOTONIC,&a)==0);usleep(1000);check("clock-advances",clock_gettime(CLOCK_MONOTONIC,&b)==0&&(b.tv_sec>a.tv_sec||(b.tv_sec==a.tv_sec&&b.tv_nsec>a.tv_nsec)));unsigned char random[32];check("entropy",syscall(SYS_getrandom,random,32,0)==32);}
 if(!strcmp(mode,"ignored-denial")){fd=socket(AF_INET,SOCK_STREAM,0);check("ignored-denial",fd<0&&errno==EPERM);}
 if(!strncmp(mode,"stdout-",7)||!strncmp(mode,"stderr-",7)){emit(mode[3]=='o'?1:2,33554432u+(strstr(mode,"overflow")?1u:0u),mode[3]=='o'?'x':'y');return 0;}
 if(!strcmp(mode,"hold")||!strcmp(mode,"wait")){puts("independent-dynamic-ready");fflush(stdout);sleep(!strcmp(mode,"hold")?5:45);puts("independent-dynamic-done");return 0;}
 if(!strcmp(mode,"failure")){puts("independent-dynamic-failure");return 9;}
 puts("independent-dynamic-pass");return 0;
}
`

type dynamicBoundaryFixture struct {
	backend                    Backend
	docker, primary, directory string
	runtime                    *Runtime
	request                    EnvelopeRequest
}

func prepareIndependentDynamicFixture(t *testing.T, ctx context.Context) dynamicBoundaryFixture {
	t.Helper()
	image, arch := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_NATIVE_ARCH")
	if image == "" {
		t.Skip("explicit immutable installed Linux GCC image required")
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
	directory := t.TempDir()
	worker, err := prepareWorker(ctx, goTool, directory, arch)
	if err != nil {
		t.Fatal(err)
	}
	f := dynamicBoundaryFixture{docker: docker, directory: directory, primary: filepath.Join(directory, "program"), backend: Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}}
	if err := f.backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "probe.c"), []byte(independentDynamicProgram), 0600); err != nil {
		t.Fatal(err)
	}
	libdir, loader := "/lib/aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libdir, loader = "/lib/x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	}
	script := "set -eu\n/usr/bin/gcc -O2 -pthread -Wl,-z,noexecstack -o /build/program /build/probe.c\nmkdir /build/libs\ncp -L " + libdir + "/libc.so.6 /build/libs/libc.so.6\ncp -L " + libdir + "/" + loader + " /build/libs/" + loader + "\n"
	out, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--mount", "type=bind,src="+directory+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("independent installed C build failed: %v %s", err, out.Stderr)
	}
	f.runtime, err = CaptureRuntime(ctx, RuntimeRequest{Loader: filepath.Join(directory, "libs", loader), Libraries: []RuntimeLibrary{{"libc.so.6", filepath.Join(directory, "libs", "libc.so.6")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	f.request, _ = envelopeFixture(t)
	f.request.Env = []string{"DECLARED=fixture", "FIXED=second"}
	f.request.ScratchBytes = 8 << 20
	code, err := os.ReadFile(f.primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.request.Files.Roots[0].Path, "identical-code"), code, 0555); err != nil {
		t.Fatal(err)
	}
	f.request.Files.Inputs = append(f.request.Files.Inputs, inputbundle.Input{Root: "source", Path: "identical-code"})
	return f
}

func (f dynamicBoundaryFixture) capture(t *testing.T, mode string) (*DynamicProgram, *Envelope) {
	t.Helper()
	args := []string{mode, "argument with spaces"}
	p, err := CaptureDynamicProgram(context.Background(), DynamicProgramRequest{Program: f.primary, Args: args, Runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "caller-mutated-argument"
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p, captureFixtureEnvelope(t, f.request)
}

func TestInstalledLinuxDynamicIndependentBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	out, err := command(ctx, f.docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--tmpfs", "/tmp:rw,mode=1777", "--mount", "type=bind,src="+f.primary+",dst=/control,readonly", "--entrypoint=/bin/sh", f.backend.Image, "-c", "printf '' | /control outside")
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "independent-dynamic-outside-pass\n") || len(out.Stderr) != 0 {
		t.Fatalf("outside syscall positive controls failed: %v %s", err, out.Stderr)
	}
	// userfaultfd may already be denied by the actual Docker/kernel policy;
	// its inside denial is attributed to this boundary as a whole.
	for _, mode := range []string{"positive", "positive", "denials", "observables", "ignored-denial", "failure"} {
		t.Run(mode, func(t *testing.T) {
			p, e := f.capture(t, mode)
			out, err := RunDynamicProgram(ctx, f.backend, p, e)
			marker, exit := "independent-dynamic-pass\n", 0
			if mode == "failure" {
				marker, exit = "independent-dynamic-failure\n", 9
			}
			if !out.Completed || out.ExitCode != exit || string(out.Stdout) != marker || len(out.Stderr) != 0 || (exit == 0 && err != nil) || (exit != 0 && err == nil) {
				t.Fatalf("dynamic mode%s complete=%v exit=%d error=%v stderr=%s", mode, out.Completed, out.ExitCode, err, out.Stderr)
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxDynamicIndependentOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	for _, mode := range []string{"stdout-maximum", "stderr-maximum", "stdout-overflow", "stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			p, e := f.capture(t, mode)
			out, err := RunDynamicProgram(ctx, f.backend, p, e)
			data, other, value := out.Stdout, out.Stderr, "x"
			if strings.HasPrefix(mode, "stderr") {
				data, other, value = out.Stderr, out.Stdout, "y"
			}
			if len(data) > 32<<20 || len(other) != 0 || strings.Trim(string(data), value) != "" {
				t.Fatal("raw stream framing or guest bounds failed", len(data), len(other), err)
			}
			if strings.HasSuffix(mode, "overflow") {
				if err == nil || out.Completed {
					t.Fatal("actual dynamic output overflow admitted")
				}
			} else if err != nil || !out.Completed || out.ExitCode != 0 || len(data) != 32<<20 {
				t.Fatal("exact dynamic output boundary rejected", len(data), err)
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func dynamicBoundaryMounts(t *testing.T, ctx context.Context, f dynamicBoundaryFixture, id string) map[string]string {
	t.Helper()
	out, err := command(ctx, f.docker, "inspect", id, "--format", "{{json .Mounts}}")
	var mounts []struct{ Source, Destination string }
	if err != nil || json.Unmarshal(out.Stdout, &mounts) != nil {
		t.Fatal("actual dynamic mounts unavailable", err)
	}
	result := map[string]string{}
	for _, mount := range mounts {
		result[mount.Destination] = mount.Source
	}
	return result
}

func waitIndependentDynamicReady(t *testing.T, ctx context.Context, f dynamicBoundaryFixture, done <-chan staticProgramResult) string {
	t.Helper()
	for {
		select {
		case <-done:
			t.Fatal("dynamic program exited before actualREADY")
		case <-ctx.Done():
			t.Fatal("dynamicREADY bound exceeded")
		default:
		}
		out, err := command(ctx, f.docker, "ps", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range strings.Fields(string(out.Stdout)) {
			logs, err := command(ctx, f.docker, "logs", id)
			if err == nil && strings.Contains(string(logs.Stdout), "independent-dynamic-ready\n") {
				return id
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mutateIndependentDynamicBytes(t *testing.T, name string) {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil || len(data) == 0 {
		t.Fatal("known copied file unavailable", err)
	}
	data[len(data)-1] ^= 1
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(name)
	if err != nil || !os.SameFile(info, after) || info.Mode() != after.Mode() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("byte-only fault changed identity or metadata", err)
	}
}

func TestInstalledLinuxDynamicIndependentLiveGuards(t *testing.T) {
	for _, fault := range []string{"healthy", "source-bytes", "source-inode", "runtime-source-bytes", "captured-copy-bytes", "transport-primary-bytes", "transport-library-bytes", "contract-bytes", "contract-mode", "resources-bytes", "resources-mode", "boundary-bytes", "cleanup"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			f := prepareIndependentDynamicFixture(t, ctx)
			p, e := f.capture(t, "hold")
			run, stop := context.WithCancel(ctx)
			done := make(chan staticProgramResult, 1)
			go func() {
				out, err := RunDynamicProgram(run, f.backend, p, e)
				done <- staticProgramResult{out, err}
				close(done)
			}()
			joined := false
			var recoverCleanup func()
			defer func() {
				stop()
				if !joined {
					select {
					case <-done:
						joined = true
					case <-time.After(50 * time.Second):
						t.Error("dynamic invocation did not join after cancellation")
					}
				}
				if joined && recoverCleanup != nil {
					recoverCleanup()
				}
			}()
			id := waitIndependentDynamicReady(t, ctx, f, done)
			mounts := dynamicBoundaryMounts(t, ctx, f, id)
			if mounts["/runtime"] == "" || mounts["/input"] == "" || mounts["/boundary"] == "" {
				t.Fatal("private dynamic runtime/control mount positive unavailable")
			}
			switch fault {
			case "source-bytes":
				mutateIndependentDynamicBytes(t, f.primary)
			case "source-inode":
				info, err := os.Lstat(f.primary)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := os.Lstat(filepath.Dir(f.primary))
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(f.primary)
				if err != nil {
					t.Fatal(err)
				}
				replacement := f.primary + ".replacement"
				if err := os.WriteFile(replacement, data, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				other, err := os.Lstat(replacement)
				if err != nil || os.SameFile(info, other) || info.Mode() != other.Mode() || info.Size() != other.Size() || !info.ModTime().Equal(other.ModTime()) {
					t.Fatal("distinct-inode fault control unavailable", err)
				}
				if err := os.Rename(replacement, f.primary); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Dir(f.primary), parent.ModTime(), parent.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "runtime-source-bytes":
				mutateIndependentDynamicBytes(t, f.runtime.libraries["libc.so.6"].source)
			case "captured-copy-bytes":
				mutateIndependentDynamicBytes(t, p.primary.copy)
			case "transport-primary-bytes":
				mutateIndependentDynamicBytes(t, filepath.Join(mounts["/runtime"], "#program"))
			case "transport-library-bytes":
				mutateIndependentDynamicBytes(t, filepath.Join(mounts["/runtime"], "libc.so.6"))
			case "contract-bytes":
				mutateIndependentDynamicBytes(t, filepath.Join(mounts["/input"], "contract.json"))
			case "contract-mode":
				if err := os.Chmod(filepath.Join(mounts["/input"], "contract.json"), 0640); err != nil {
					t.Fatal(err)
				}
			case "resources-bytes":
				mutateIndependentDynamicBytes(t, filepath.Join(mounts["/input"], "resources.json"))
			case "resources-mode":
				if err := os.Chmod(filepath.Join(mounts["/input"], "resources.json"), 0640); err != nil {
					t.Fatal(err)
				}
			case "boundary-bytes":
				mutateIndependentDynamicBytes(t, mounts["/boundary"])
			case "cleanup":
				root := mounts["/input"]
				if !strings.HasPrefix(filepath.Base(root), "hayaku-native-envelope-") {
					t.Fatal("controlled cleanup root is not this invocation's private envelope")
				}
				if err := os.Chmod(mounts["/runtime"], 0500); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				recoverCleanup = func() {
					os.Chmod(root, 0700)
					os.Chmod(mounts["/runtime"], 0700)
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				}
			}
			var result staticProgramResult
			select {
			case result = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("dynamic successful-program postguard exceeded original deadline")
			}
			if result.out.ExitCode != 0 || string(result.out.Stdout) != "independent-dynamic-ready\nindependent-dynamic-done\n" {
				t.Fatalf("actual runtime did not successfully complete known arithmetic control: %v %s", result.err, result.out.Stdout)
			}
			if fault == "healthy" {
				if result.err != nil || !result.out.Completed {
					t.Fatal("healthy live program guard rejected", result.err)
				}
			} else {
				if result.err == nil || result.out.Completed {
					t.Fatal("actual source/copy/control/cleanup drift remained complete", fault)
				}
			}
			if fault == "contract-bytes" || fault == "boundary-bytes" || fault == "resources-bytes" {
				if !strings.Contains(result.err.Error(), "native copied control bytes changed") {
					t.Fatal("copied control byte fault masked by unrelated guard", result.err)
				}
			}
			if (fault == "contract-mode" || fault == "resources-mode") && !strings.Contains(result.err.Error(), "native copied control changed") {
				t.Fatal("control mode fault masked", result.err)
			}
			if fault == "cleanup" && !strings.Contains(result.err.Error(), "cleanup") {
				t.Fatal("cleanup fault masked", result.err)
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxDynamicIndependentDeadlineAndCancel(t *testing.T) {
	for _, mode := range []string{"cancel", "cancel-and-control-drift", "cancel-and-cleanup", "fixed-timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			f := prepareIndependentDynamicFixture(t, ctx)
			p, e := f.capture(t, "wait")
			run, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan staticProgramResult, 1)
			go func() {
				out, err := RunDynamicProgram(run, f.backend, p, e)
				done <- staticProgramResult{out, err}
				close(done)
			}()
			joined := false
			var recoverCleanup func()
			defer func() {
				stop()
				if !joined {
					select {
					case <-done:
						joined = true
					case <-time.After(50 * time.Second):
						t.Error("dynamic canceled runtime did not join")
					}
				}
				if joined && recoverCleanup != nil {
					recoverCleanup()
				}
			}()
			id := waitIndependentDynamicReady(t, ctx, f, done)
			if mode == "cancel-and-control-drift" {
				mounts := dynamicBoundaryMounts(t, ctx, f, id)
				mutateIndependentDynamicBytes(t, filepath.Join(mounts["/input"], "contract.json"))
			}
			if mode == "cancel-and-cleanup" {
				mounts := dynamicBoundaryMounts(t, ctx, f, id)
				root := mounts["/input"]
				if !strings.HasPrefix(filepath.Base(root), "hayaku-native-envelope-") {
					t.Fatal("cleanup fault root is not this private invocation")
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				recoverCleanup = func() {
					os.Chmod(root, 0700)
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				}
			}
			if strings.HasPrefix(mode, "cancel") {
				stop()
			}
			var result staticProgramResult
			select {
			case result = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("dynamic cancel/fixed30-second deadline exceeded parent bound")
			}
			cause := context.Canceled
			if mode == "fixed-timeout" {
				cause = context.DeadlineExceeded
			}
			if !errors.Is(result.err, cause) || result.out.Completed || !strings.Contains(string(result.out.Stdout), "independent-dynamic-ready\n") || strings.Contains(string(result.out.Stdout), "independent-dynamic-done\n") {
				t.Fatal("active dynamic cancellation lost typed cause or completed", result.err)
			}
			if mode == "cancel-and-control-drift" && !strings.Contains(result.err.Error(), "native copied control bytes changed") {
				t.Fatal("combined control fault lost its independent guard error", result.err)
			}
			if mode == "cancel-and-cleanup" && !strings.Contains(result.err.Error(), "cleanup") {
				t.Fatal("combined cleanup fault not reached", result.err)
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxDynamicIndependentLoaderEnvironmentRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	p, e := f.capture(t, "positive")
	out, err := RunDynamicProgram(ctx, f.backend, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "independent-dynamic-pass\n" {
		t.Fatal("healthy declared environment control failed", err)
	}
	for _, key := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "GLIBC_TUNABLES"} {
		t.Run(key, func(t *testing.T) {
			request := f.request
			request.Env = append(append([]string(nil), request.Env...), key+"=untrusted")
			envelope := captureFixtureEnvelope(t, request)
			out, err := RunDynamicProgram(ctx, f.backend, p, envelope)
			if err == nil || out.Completed || len(out.Stdout) != 0 || !strings.Contains(err.Error(), "ambient loader environment is unsupported") {
				t.Fatal("ambient loader environment reached native launch", err)
			}
			assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, envelope)
			assertNoManagedContainers(t, f.docker)
		})
	}
}

const independentDynamicNode = `
'use strict';
const fs=require('node:fs'),fsp=require('node:fs/promises'),path=require('node:path'),net=require('node:net');
const {Worker}=require('node:worker_threads');const value=require('./module.cjs');
function check(name,ok){if(!ok)throw Error('node-independent-failed:'+name)}
async function main(){
 const mode=process.argv[2];check('declared-mode',['outside','positive','denials','failure','hold','wait'].includes(mode));check('jitless',process.execArgv.includes('--jitless'));
 check('environment',Object.keys(process.env).sort().join(',')==='DECLARED,FIXED'&&process.env.DECLARED==='fixture'&&process.env.FIXED==='second');
 check('module',value===7);check('async-resource',(await fsp.readFile(path.join(__dirname,'data/value'),'utf8'))==='fixture');
 const thread=new Worker(path.join(__dirname,'worker.cjs'));
 const message=await new Promise((resolve,reject)=>{thread.once('message',resolve);thread.once('error',reject)});await thread.terminate();check('worker-thread',message==='fixture');
 if(mode==='outside'){
  check('outside-content',fs.readFileSync('/etc/os-release','utf8').length>0);
  const server=net.createServer();await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve)});await new Promise(resolve=>server.close(resolve));console.log('node-independent-outside-pass');return;
 }
 check('captured-openssl-config',process.execArgv.includes('--openssl-config=/resources/openssl.cnf')&&fs.readFileSync('/resources/openssl.cnf','utf8')==='# Hayaku independent declared OpenSSL configuration\n');
 check('cwd',process.cwd()==='/resources');// Node rewrites argv[0] to execPath. Its original kernel argv[0] is argv0.
 check('initial-argv',process.argv0==='/runtime/#program'&&process.argv.length===3&&process.argv[1]==='/resources/main.cjs');
 check('manual-loader-observable',process.argv[0]===process.execPath&&process.execPath==='/runtime/'+(process.arch==='arm64'?'ld-linux-aarch64.so.1':'ld-linux-x86-64.so.2'));
 check('empty-stdin',fs.readFileSync(0).length===0);
 check('fresh-scratch',!fs.existsSync('/scratch/node-state'));fs.writeFileSync('/scratch/node-state','scratch');check('scratch-read',fs.readFileSync('/scratch/node-state','utf8')==='scratch');
 if(mode==='denials'){
  let denied=false;try{fs.readFileSync('/etc/os-release')}catch(error){denied=['EACCES','EPERM'].includes(error.code)}check('outside-read-denied',denied);
  const server=net.createServer();const failure=await new Promise(resolve=>{server.once('error',resolve);server.listen(0,'127.0.0.1',()=>resolve(null))});if(!failure)await new Promise(resolve=>server.close(resolve));check('socket-denied',failure&&['EACCES','EPERM'].includes(failure.code));
  const fd=fs.openSync('/scratch/node-state','r+');denied=false;try{fs.fchmodSync(fd,0o700)}catch(error){denied=error.code==='EPERM'}finally{fs.closeSync(fd)}check('metadata-mutation-denied',denied);
 }
 if(mode==='hold'||mode==='wait'){console.log('node-independent-ready');await new Promise(resolve=>setTimeout(resolve,mode==='hold'?5000:45000));console.log('node-independent-done');return;}
 if(mode==='failure'){check('seeded-runtime-assertion',value===8);}
 console.log('node-independent-pass');
}
// Synchronous diagnostic output must not mask a failure with Node's separate
// console/Socket initialization under this modified operational profile.
main().catch(error=>{fs.writeSync(2,String(error.stack)+'\n');process.exitCode=9});
`

func TestInstalledLinuxDynamicIndependentNodeJitless(t *testing.T) {
	image, node, expected := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_DYNAMIC_NODE"), os.Getenv("HAYAKU_DYNAMIC_NODE_SHA256")
	if image == "" || node == "" || expected == "" {
		t.Skip("explicit installed Linux Node and official verified executable digest required")
	}
	data, err := os.ReadFile(node)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		t.Fatal("installed Node differs from explicit verified identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
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
	resources := filepath.Join(dir, "resources")
	if err := os.MkdirAll(filepath.Join(resources, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"main.cjs": independentDynamicNode, "module.cjs": "module.exports=7;", "worker.cjs": "require('node:worker_threads').parentPort.postMessage(require('node:fs').readFileSync(__dirname+'/data/value','utf8'));", "data/value": "fixture", "openssl.cnf": "# Hayaku independent declared OpenSSL configuration\n"} {
		if err := os.WriteFile(filepath.Join(resources, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--mount", "type=bind,src="+resources+",dst=/fixture,readonly", "--entrypoint=/usr/bin/env", image, "-i", "DECLARED=fixture", "FIXED=second", "/node", "--jitless", "/fixture/main.cjs", "outside")
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "node-independent-outside-pass\n" {
		t.Fatalf("original jitless module/async/thread/content/socket control failed: %v %s", err, out.Stderr)
	}
	// Explicitly declared configuration is a modified startup profile. Retain
	// the default outside control above and independently run this same option
	// against the ordinary executable before the captured manual-loader run.
	out, err = command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--mount", "type=bind,src="+resources+",dst=/resources,readonly", "--entrypoint=/usr/bin/env", image, "-i", "DECLARED=fixture", "FIXED=second", "/node", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", "outside")
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "node-independent-outside-pass\n" {
		t.Fatalf("configured original jitless positive failed: %v %s", err, out.Stderr)
	}
	version, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--entrypoint=/node", image, "--version")
	if err != nil || !version.Completed || version.ExitCode != 0 || string(version.Stdout) != "v22.18.0\n" {
		t.Fatal("actual pinned Node version control failed", err)
	}
	libdir, loader := "/lib/aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libdir, loader = "/lib/x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	}
	libraries := []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}
	script := "set -eu\nmkdir /build/libs\ncp -L " + libdir + "/" + loader + " /build/libs/" + loader + "\n"
	for _, name := range libraries {
		script += "cp -L " + libdir + "/" + name + " /build/libs/" + name + "\n"
	}
	out, err = command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--mount", "type=bind,src="+dir+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatal("exact installed runtime library capture failed", err)
	}
	declaration := RuntimeRequest{Loader: filepath.Join(dir, "libs", loader)}
	for _, name := range libraries {
		declaration.Libraries = append(declaration.Libraries, RuntimeLibrary{name, filepath.Join(dir, "libs", name)})
	}
	runtime, err := CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	request := EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: resources, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}}, Env: []string{"DECLARED=fixture", "FIXED=second"}, ScratchBytes: 8 << 20}
	envelope := captureFixtureEnvelope(t, request)
	backend := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	// The default configuration stays outside the declared resource closure.
	// Rejection is an operational limitation; it does not justify granting
	// default Node startup access to ambient system configuration.
	defaultProgram, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: node, Args: []string{"--jitless", "/resources/main.cjs", "positive"}, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	out, err = RunDynamicProgram(ctx, backend, defaultProgram, envelope)
	if closeErr := defaultProgram.Close(); closeErr != nil {
		t.Error(closeErr)
	}
	if err == nil || out.ExitCode != 13 || len(out.Stdout) != 0 || !strings.Contains(string(out.Stderr), "Permission denied") || !strings.Contains(string(out.Stderr), "fopen(/etc/ssl/openssl.cnf") {
		t.Fatalf("default startup must expose undeclared OpenSSL access: %v %s", err, out.Stderr)
	}
	assertNoManagedContainers(t, docker)
	for _, mode := range []string{"positive", "positive", "denials", "failure"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", mode}
			program, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: node, Args: args, Runtime: runtime})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := program.Close(); err != nil {
					t.Error(err)
				}
			}()
			args[3] = "caller-mutated-arguments"
			out, err := RunDynamicProgram(ctx, backend, program, envelope)
			if mode == "failure" {
				if err == nil || !out.Completed || out.ExitCode != 9 || !strings.Contains(string(out.Stderr), "node-independent-failed:seeded-runtime-assertion") {
					t.Fatal("actual Node runtime assertion failure not preserved", err)
				}
			} else if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "node-independent-pass\n" {
				t.Fatalf("jitless profile failed %s: %v %s", mode, err, out.Stderr)
			}
			assertManagedEnvelopeNoAuthority(t, backend, node, envelope)
			assertNoManagedContainers(t, docker)
		})
	}
}

func TestInstalledLinuxDynamicIndependentAliasRetarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	alias := filepath.Join(f.directory, "declared-program")
	other := filepath.Join(f.directory, "identical-program")
	data, err := os.ReadFile(f.primary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.primary, alias); err != nil {
		t.Fatal(err)
	}
	p, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: alias, Args: []string{"positive", "argument with spaces"}, Runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	e := captureFixtureEnvelope(t, f.request)
	out, err := RunDynamicProgram(ctx, f.backend, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "independent-dynamic-pass\n" {
		t.Fatal("healthy actual alias launch did not work", err)
	}
	parent, err := os.Stat(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.directory, parent.ModTime(), parent.ModTime()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(f.primary)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(other)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("identical-byte distinct alias target control unavailable", err)
	}
	out, err = RunDynamicProgram(ctx, f.backend, p, e)
	if err == nil || out.Completed || len(out.Stdout) != 0 || !strings.Contains(err.Error(), "dynamic source") {
		t.Fatal("retargeted alias reached runtime or was masked by missing backend", err)
	}
	assertNoManagedContainers(t, f.docker)
}

func TestInstalledLinuxDynamicIndependentAggregateBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	p, _ := f.capture(t, "positive")
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		file, err := os.OpenFile(filepath.Join(root, strings.Repeat("a", i+1)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(64 << 20); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	e := captureFixtureEnvelope(t, EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "bounded", Path: root, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "bounded", Path: "."}}}})
	if e.files.Manifest().Bytes != inputbundle.MaxBytes || p.primary.file.Size() <= 0 || f.runtime.bytes <= 0 {
		t.Fatal("actual independent exact512MiB envelope/runtime positive controls missing")
	}
	out, err := RunDynamicProgram(ctx, f.backend, p, e)
	if err == nil || out.Completed || len(out.Stdout) != 0 || len(out.Stderr) != 0 || err.Error() != "dynamic inputs exceed aggregate bounds" {
		t.Fatal("actual resource+runtime aggregate reached execution or wrong guard", err)
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
	assertNoManagedContainers(t, f.docker)
}

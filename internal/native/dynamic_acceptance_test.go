package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/process"
)

// The newer syscall numbers/argument layouts are pinned to the upstream Linux
// v6.17 UAPI: include/uapi/asm-generic/unistd.h, linux/xattr.h, linux/fs.h and
// include/linux/syscalls.h. Outside unavailable calls are recorded explicitly;
// they cannot establish attribution to Hayaku's added filter.
const dynamicAcceptanceProgram = `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <signal.h>
#include <sched.h>
#include <sys/mman.h>
#include <sys/ptrace.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/uio.h>
#include <sys/wait.h>
#include <sys/xattr.h>
#include <linux/io_uring.h>
#include <linux/sched.h>
static int inside;
static void check(const char *name,int ok){if(!ok){fprintf(stderr,"extended-boundary-failed:%s errno=%d\n",name,errno);exit(73);}}
static int unavailable(int e){return e==ENOSYS||e==EPERM||e==EACCES||e==EOPNOTSUPP;}
static void result(const char *name,long value,int success){int e=errno;if(inside){check(name,value<0&&e==EPERM);return;}check(name,success||(value<0&&unavailable(e)));printf("outside-%s-%s\n",name,success?"available":"unavailable");}
static void file_controls(void){
 const char *path=inside?"/scratch/attributes":"/tmp/attributes";
 int fd=open(path,O_CREAT|O_RDWR|O_EXCL,0600);check("fresh-owned-file",fd>=0);check("owned-file-write",write(fd,"owned",5)==5);
 struct stat before,after;check("owned-file-stat",fstat(fd,&before)==0);
 long rc=syscall(452,AT_FDCWD,path,0700,0);result("fchmodat2",rc,rc==0);if(!inside&&rc==0){check("outside-chmod-consumed",fstat(fd,&after)==0&&(after.st_mode&0777)==0700);}
 struct {uint64_t value;uint32_t size,flags;} args={(uintptr_t)"x",1,0};
 rc=syscall(463,AT_FDCWD,path,0,"user.hayaku",&args,sizeof(args));result("setxattrat",rc,rc==0);
 if(!inside && rc==0){char value=0;check("outside-xattr-consumed",getxattr(path,"user.hayaku",&value,1)==1&&value=='x');}
 rc=syscall(466,AT_FDCWD,path,0,"user.hayaku");
 if(!inside && rc<0 && errno==ENODATA){puts("outside-removexattrat-unavailable-without-set-control");}else{result("removexattrat",rc,rc==0);if(!inside&&rc==0){char value;errno=0;check("outside-removal-consumed",getxattr(path,"user.hayaku",&value,1)<0&&errno==ENODATA);}}
 struct {uint64_t flags;uint32_t extsize,nextents,projid,cowextsize;} attr={0};
 rc=syscall(469,AT_FDCWD,path,&attr,sizeof(attr),0);result("file-setattr",rc,rc==0);
 if(inside){check("metadata-denials-preserve-file",fstat(fd,&after)==0&&before.st_dev==after.st_dev&&before.st_ino==after.st_ino&&before.st_mode==after.st_mode&&before.st_size==after.st_size&&before.st_mtim.tv_sec==after.st_mtim.tv_sec&&before.st_mtim.tv_nsec==after.st_mtim.tv_nsec);char value=0;check("denials-preserve-bytes",pread(fd,&value,1,0)==1&&value=='o');}
 close(fd);
}
static void memory_controls(void){
 void *page=mmap(NULL,4096,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("rw-positive",page!=MAP_FAILED);((char*)page)[0]=17;
 check("mprotect-rw-positive",mprotect(page,4096,PROT_READ|PROT_WRITE)==0);
 long rc=syscall(SYS_pkey_mprotect,page,4096,PROT_READ|PROT_EXEC,-1);result("pkey-exec",rc,rc==0);
 if(!inside&&rc==0)check("pkey-restore",mprotect(page,4096,PROT_READ|PROT_WRITE)==0);
 char src='a',dst=0;struct iovec local={&dst,1},remote={&src,1};
 rc=syscall(SYS_process_vm_readv,getpid(),&local,1,&remote,1,0);result("process-vm-read",rc,rc==1&&dst=='a');
 src='b';dst='a';rc=syscall(SYS_process_vm_writev,getpid(),&remote,1,&local,1,0);result("process-vm-write",rc,rc==1&&dst=='b');
 if(inside)check("remote-memory-denials-preserve-bytes",dst=='a');
 munmap(page,4096);
}
static void process_controls(void){
 if(inside){unsigned long flags[]={0,SIGCHLD,CLONE_VM|SIGCHLD,0x3d0f00UL|CLONE_NEWUSER,0x50f00UL|CLONE_NEWUSER,0x3d0f00UL|(1UL<<32),0x50f00UL|(1UL<<32)};
  for(size_t i=0;i<sizeof(flags)/sizeof(flags[0]);i++){errno=0;long rc=syscall(SYS_clone,flags[i],NULL,NULL,NULL,NULL);check("forbidden-clone-flags",rc<0&&errno==EPERM);}
  struct clone_args args={0};args.exit_signal=SIGCHLD;errno=0;long rc=syscall(435,&args,sizeof(args));check("clone3-explicit-fallback",rc<0&&errno==ENOSYS);
 }else{pid_t pid=syscall(SYS_clone,SIGCHLD,NULL,NULL,NULL,NULL);check("outside-clone-process",pid>=0);if(pid==0)syscall(SYS_exit,0);int status;check("outside-clone-reaped",waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0);puts("outside-clone-process-available");}
 int original=open(inside?"data/value":"/etc/os-release",O_RDONLY);check("pidfd-source-positive",original>=0);
 int pidfd=syscall(434,getpid(),0);check("pidfd-self-positive",pidfd>=0);
 long lent=syscall(438,pidfd,original,0);result("pidfd-lending",lent,lent>=0);if(lent>=0)close(lent);close(pidfd);close(original);
 struct io_uring_params p={0};long ring=syscall(425,2,&p);result("io-uring-setup",ring,ring>=0);if(ring>=0)close(ring);
 if(inside){errno=0;long rc=syscall(426,-1,0,0,0,NULL,0);check("io-uring-enter-denied",rc<0&&errno==EPERM);errno=0;rc=syscall(427,-1,0,NULL,0);check("io-uring-register-denied",rc<0&&errno==EPERM);}
}
static void descriptor_controls(void){
 int pair[2];long rc=socketpair(AF_UNIX,SOCK_STREAM,0,pair);result("scm-channel",rc,rc==0);if(!inside)check("outside-scm-required",rc==0);if(rc<0)return;
 int fd=open("/etc/os-release",O_RDONLY);check("outside-scm-source",fd>=0);
 char data='f';struct iovec iov={&data,1};union{struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(int))];} control={0};
 struct msghdr msg={0};msg.msg_iov=&iov;msg.msg_iovlen=1;msg.msg_control=control.bytes;msg.msg_controllen=sizeof(control.bytes);
 struct cmsghdr *c=CMSG_FIRSTHDR(&msg);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(sizeof(int));memcpy(CMSG_DATA(c),&fd,sizeof(fd));
 check("outside-scm-send",sendmsg(pair[0],&msg,0)==1);memset(control.bytes,0,sizeof(control.bytes));check("outside-scm-receive",recvmsg(pair[1],&msg,0)==1);
 c=CMSG_FIRSTHDR(&msg);check("outside-scm-header",c&&c->cmsg_level==SOL_SOCKET&&c->cmsg_type==SCM_RIGHTS&&c->cmsg_len==CMSG_LEN(sizeof(int)));int borrowed=-1;memcpy(&borrowed,CMSG_DATA(c),sizeof(borrowed));char b[16];check("outside-scm-content",borrowed>=0&&read(borrowed,b,sizeof(b))>0);close(borrowed);close(fd);close(pair[0]);close(pair[1]);
}
static void *thread(void *unused){
 char data[8];int fd=open(inside?"data/value":"/etc/os-release",O_RDONLY);check("thread-declared-positive",fd>=0&&read(fd,data,sizeof(data))>0);close(fd);
 if(inside){fd=open("/etc/os-release",O_RDONLY);check("thread-outside-denied",fd<0&&(errno==EACCES||errno==EPERM));fd=open("/input/contract.json",O_RDONLY);check("thread-control-denied",fd<0&&(errno==EACCES||errno==EPERM));char a='a',b=0;struct iovec local={&b,1},remote={&a,1};errno=0;long rc=syscall(SYS_process_vm_readv,getpid(),&local,1,&remote,1,0);check("thread-vm-denied",rc<0&&errno==EPERM&&b==0);}
 return NULL;
}
int main(int argc,char **argv){
 check("arguments",argc>=2);inside=strcmp(argv[1],"outside")!=0;
 pthread_t th;check("pthread-positive",pthread_create(&th,NULL,thread,NULL)==0);check("pthread-join",pthread_join(th,NULL)==0);
 file_controls();memory_controls();process_controls();descriptor_controls();
 errno=0;long rc=syscall(SYS_ptrace,PTRACE_TRACEME,0,NULL,NULL);result("ptrace",rc,rc==0);
 puts(inside?"extended-dynamic-inside-pass":"extended-dynamic-outside-pass");return 0;
}
`

func additionalContainerName(f dynamicBoundaryFixture, role string) string {
	id := sha256.Sum256([]byte(f.directory))
	return "hayaku-dynamic-acceptance-" + hex.EncodeToString(id[:12]) + "-" + role
}

// Return only the authored probe's bounded failure tag and numeric errno;
// compiler diagnostics or unexpected payloads cannot leak into test reports.
func additionalFailureTag(stderr []byte) string {
	if len(stderr) > 128 {
		return "unavailable"
	}
	fields := strings.Fields(string(stderr))
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "extended-boundary-failed:") || !strings.HasPrefix(fields[1], "errno=") {
		return "unavailable"
	}
	tag := strings.TrimPrefix(fields[0], "extended-boundary-failed:")
	number := strings.TrimPrefix(fields[1], "errno=")
	if tag == "" || number == "" {
		return "unavailable"
	}
	for _, v := range tag {
		if (v < 'a' || v > 'z') && v != '-' {
			return "unavailable"
		}
	}
	for _, v := range number {
		if v < '0' || v > '9' {
			return "unavailable"
		}
	}
	return fields[0] + " " + fields[1]
}

// runAdditionalContainer owns each fixture guest independently of the Docker
// client's lifetime. Cleanup has a bounded background context and verifies the
// exact assigned name is absent even when the original invocation times out.
func runAdditionalContainer(t *testing.T, ctx context.Context, f dynamicBoundaryFixture, role string, args ...string) (process.Output, error) {
	t.Helper()
	name := additionalContainerName(f, role)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = command(cleanup, f.docker, "rm", "-f", name)
		inventory, err := command(cleanup, f.docker, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if err != nil || !inventory.Completed || inventory.ExitCode != 0 || len(inventory.Stdout) != 0 || len(inventory.Stderr) != 0 {
			t.Error("additional fixture guest cleanup not verified")
		}
	})
	argv := []string{"run", "--rm", "--name", name, "--pull=never", "--network=none", "--pids-limit=128", "--memory=512m", "--cpus=2"}
	return command(ctx, f.docker, append(argv, args...)...)
}

func prepareAdditionalDynamicFixture(t *testing.T, ctx context.Context) dynamicBoundaryFixture {
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
	if err := os.WriteFile(filepath.Join(directory, "additional.c"), []byte(dynamicAcceptanceProgram), 0600); err != nil {
		t.Fatal(err)
	}
	libdir, loader := "/lib/aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libdir, loader = "/lib/x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	}
	script := "set -eu\n/usr/bin/gcc -O2 -pthread -Wl,-z,noexecstack -o /build/program /build/additional.c\nmkdir /build/libs\ncp -L " + libdir + "/libc.so.6 /build/libs/libc.so.6\ncp -L " + libdir + "/" + loader + " /build/libs/" + loader + "\n"
	out, err := runAdditionalContainer(t, ctx, f, "build", "--mount", "type=bind,src="+directory+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("additional probe build failed: complete=%v exit=%d error=%v", out.Completed, out.ExitCode, err)
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
	return f
}

func TestInstalledLinuxDynamicAdditionalSyscallAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareAdditionalDynamicFixture(t, ctx)
	out, err := runAdditionalContainer(t, ctx, f, "control", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--tmpfs", "/tmp:rw,mode=1777", "--mount", "type=bind,src="+f.primary+",dst=/control,readonly", "--entrypoint=/control", f.backend.Image, "outside")
	if err != nil || !out.Completed || out.ExitCode != 0 || len(out.Stderr) != 0 || !strings.HasSuffix(string(out.Stdout), "extended-dynamic-outside-pass\n") {
		t.Fatalf("additional outside controls failed: complete=%v exit=%d error=%v tag=%s", out.Completed, out.ExitCode, err, additionalFailureTag(out.Stderr))
	}
	for _, name := range []string{"fchmodat2", "setxattrat", "removexattrat", "file-setattr", "pkey-exec", "process-vm-read", "process-vm-write", "pidfd-lending", "io-uring-setup", "scm-channel", "ptrace"} {
		prefix := "outside-" + name + "-"
		available := strings.Contains(string(out.Stdout), prefix+"available\n")
		unavailable := strings.Contains(string(out.Stdout), prefix+"unavailable")
		if available == unavailable {
			t.Fatalf("outside availability record missing/ambiguous: %s", name)
		}
		if unavailable {
			t.Logf("outside %s unavailable in this kernel/container context; inside denial is whole-boundary evidence only", name)
		}
	}
	for _, mode := range []string{"inside", "inside"} {
		p, e := f.capture(t, mode)
		out, err := RunDynamicProgram(ctx, f.backend, p, e)
		if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "extended-dynamic-inside-pass\n" || len(out.Stderr) != 0 {
			t.Fatalf("additional boundary checks failed: complete=%v exit=%d error=%v tag=%s", out.Completed, out.ExitCode, err, additionalFailureTag(out.Stderr))
		}
		assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
		assertNoManagedContainers(t, f.docker)
	}
}

func TestInstalledLinuxDynamicAdditionalGuestCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareAdditionalDynamicFixture(t, ctx)
	t.Run("cancel-after-guest-start", func(t *testing.T) {
		stopped, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		out, err := runAdditionalContainer(t, stopped, f, "cancel", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--entrypoint=/bin/sh", f.backend.Image, "-c", "printf additional-fixture-started; exec sleep 45")
		if out.Completed || !errors.Is(err, context.DeadlineExceeded) || stopped.Err() != context.DeadlineExceeded || !strings.Contains(string(out.Stdout), "additional-fixture-started") {
			t.Fatal("fixture cancellation did not follow actual guest startup")
		}
	})
	// The child subtest's cleanup has already joined the known guest here.
	inventory, err := command(ctx, f.docker, "ps", "--all", "--filter", "name=^/"+additionalContainerName(f, "cancel")+"$", "--format", "{{.ID}}")
	if err != nil || !inventory.Completed || inventory.ExitCode != 0 || len(inventory.Stdout) != 0 || len(inventory.Stderr) != 0 {
		t.Fatal("cancelled guest remained after bounded cleanup")
	}
}

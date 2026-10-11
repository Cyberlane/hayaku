package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

// This independently authored probe checks a trusted compiler's operational
// memory profile. It supplies no language proof or reusable test-pass authority.
const independentManagedCompiler = `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/wait.h>
static void check(const char *name,int ok){if(!ok){fprintf(stderr,"managed-probe-failed:%s errno=%d\n",name,errno);exit(77);}}
static void code(void *p){
#if defined(__aarch64__)
 const uint32_t bytes[]={0x528000e0,0xd65f03c0};
#elif defined(__x86_64__)
 const unsigned char bytes[]={0xb8,7,0,0,0,0xc3};
#else
#error unsupported managed compiler probe architecture
#endif
 memcpy(p,bytes,sizeof(bytes));__builtin___clear_cache((char*)p,(char*)p+sizeof(bytes));
}
static void compiler_memory(int allowed){
 void *p=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);
 check("anonymous-rx",allowed?p!=MAP_FAILED:p==MAP_FAILED&&errno==EPERM);if(p!=MAP_FAILED)munmap(p,4096);
 p=mmap(NULL,4096,PROT_READ|PROT_WRITE|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);
 check("anonymous-rwx",allowed?p!=MAP_FAILED:p==MAP_FAILED&&errno==EPERM);
 if(p!=MAP_FAILED){code(p);check("rwx-generated-code",((int(*)(void))p)()==7);munmap(p,4096);}
 p=mmap(NULL,4096,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("rw-positive",p!=MAP_FAILED);code(p);
 int rc=mprotect(p,4096,PROT_READ|PROT_EXEC);check("mprotect-exec",allowed?rc==0:rc<0&&errno==EPERM);
 if(rc==0){check("rx-generated-code",((int(*)(void))p)()==7);check("restore-rw",mprotect(p,4096,PROT_READ|PROT_WRITE)==0);}
 rc=syscall(SYS_pkey_mprotect,p,4096,PROT_READ|PROT_EXEC,-1);
 // The arm64 UAPI may expose pkey_mprotect while the installed kernel lacks
 // pkeys. ENOSYS outside/managed is explicit unavailability, never a success.
 if(allowed&&rc<0&&errno==ENOSYS){puts("managed-pkey-unavailable");}
 else{check("pkey-exec",allowed?rc==0:rc<0&&errno==EPERM);if(rc==0)check("pkey-generated-code",((int(*)(void))p)()==7);}
 munmap(p,4096);
}
static void confined(void){
 int fd=open("/runtime/#program",O_RDONLY);check("runtime-read-positive",fd>=0);
 void *p=mmap(NULL,4096,PROT_READ|PROT_WRITE|PROT_EXEC,MAP_PRIVATE,fd,0);check("file-backed-rwx-denied",p==MAP_FAILED&&errno==EPERM);close(fd);
 fd=open("identical-code",O_RDONLY);check("resource-read-positive",fd>=0);
 p=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("resource-noexec",p==MAP_FAILED&&(errno==EACCES||errno==EPERM));
 p=mmap(NULL,4096,PROT_READ,MAP_PRIVATE,fd,0);check("resource-read-map-positive",p!=MAP_FAILED);
 check("resource-read-map-to-rx-noexec",mprotect(p,4096,PROT_READ|PROT_EXEC)<0&&(errno==EACCES||errno==EPERM));
 check("resource-read-map-to-rwx-noexec",mprotect(p,4096,PROT_READ|PROT_WRITE|PROT_EXEC)<0&&(errno==EACCES||errno==EPERM));munmap(p,4096);close(fd);
 fd=open("/scratch/code",O_CREAT|O_RDWR,0600);check("scratch-write-positive",fd>=0);check("scratch-size",ftruncate(fd,4096)==0);
 p=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("scratch-noexec",p==MAP_FAILED&&(errno==EACCES||errno==EPERM));
 p=mmap(NULL,4096,PROT_READ,MAP_PRIVATE,fd,0);check("scratch-read-map-positive",p!=MAP_FAILED);
 check("scratch-read-map-to-rx-noexec",mprotect(p,4096,PROT_READ|PROT_EXEC)<0&&(errno==EACCES||errno==EPERM));
 check("scratch-read-map-to-rwx-noexec",mprotect(p,4096,PROT_READ|PROT_WRITE|PROT_EXEC)<0&&(errno==EACCES||errno==EPERM));munmap(p,4096);close(fd);
 fd=open("/etc/os-release",O_RDONLY);check("external-file-denied",fd<0&&(errno==EACCES||errno==EPERM));
 fd=open("/input/contract.json",O_RDONLY);check("control-file-denied",fd<0&&(errno==EACCES||errno==EPERM));
 fd=socket(AF_INET,SOCK_STREAM,0);check("network-denied",fd<0&&errno==EPERM);
 fd=syscall(SYS_memfd_create,"managed-extra-code",0);check("memfd-denied",fd<0&&errno==EPERM);
 pid_t pid=fork();check("process-denied",pid<0&&errno==EPERM);
 char *args[]={"/runtime/#program","spawn",NULL};execv(args[0],args);check("exec-denied",errno==EACCES||errno==EPERM);
 fd=open("/runtime/#addon-unlisted.node",O_RDONLY);check("extra-addon-absent",fd<0&&errno==ENOENT);
}
static void outside(const char *self){
 int fd=open("/etc/os-release",O_RDONLY);check("outside-file-positive",fd>=0);close(fd);
 fd=socket(AF_INET,SOCK_STREAM,0);check("outside-network-positive",fd>=0);close(fd);
 fd=syscall(SYS_memfd_create,"managed-outside-control",0);check("outside-memfd-positive",fd>=0);close(fd);
 pid_t pid=fork();check("outside-process-positive",pid>=0);if(pid==0){char *args[]={(char*)self,"spawn",NULL};execv(self,args);_exit(78);}int status;check("outside-child-joined",waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0);
}
int main(int argc,char **argv){
 check("mode",argc>=2);const char *mode=argv[1];if(!strcmp(mode,"spawn"))return 0;int original=!strncmp(mode,"outside",7),strict=!strcmp(mode,"strict");
 compiler_memory(!strict);if(original)outside(argv[0]);else confined();
 if(!strcmp(mode,"hold")){puts("independent-dynamic-ready");fflush(stdout);sleep(5);puts("independent-dynamic-done");return 0;}
 if(!strcmp(mode,"wait")){puts("independent-dynamic-ready");fflush(stdout);sleep(45);return 0;}
 if(!strcmp(mode,"overflow")){char block[8192];memset(block,'x',sizeof(block));for(size_t n=0;n<33554433u;){size_t amount=33554433u-n;if(amount>sizeof(block))amount=sizeof(block);ssize_t written=write(1,block,amount);if(written<=0)return 78;n+=written;}return 0;}
 if(strstr(mode,"failure")){puts("managed-compiler-failure");return 9;}
 puts("managed-compiler-pass");return 0;
}
`

func requireManagedFixture(t *testing.T, node bool) {
	t.Helper()
	unavailable := ""
	if os.Getenv("HAYAKU_NATIVE_IMAGE") == "" {
		unavailable = "installed immutable Linux GCC/runtime image required"
	} else if node && (os.Getenv("HAYAKU_DYNAMIC_NODE") == "" || os.Getenv("HAYAKU_DYNAMIC_NODE_SHA256") == "") {
		unavailable = "installed official checksum-pinned Linux Node required"
	}
	if unavailable != "" {
		if os.Getenv("HAYAKU_MANAGED_COMPILER_REQUIRED") == "1" {
			t.Fatal(unavailable)
		}
		t.Skip(unavailable)
	}
}

func prepareManagedCompilerFixture(t *testing.T, ctx context.Context) dynamicBoundaryFixture {
	t.Helper()
	requireManagedFixture(t, false)
	f := prepareIndependentDynamicFixture(t, ctx)
	if err := os.WriteFile(filepath.Join(f.directory, "managed.c"), []byte(independentManagedCompiler), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runAdditionalContainer(t, ctx, f, "managed-build", "--mount", "type=bind,src="+f.directory+",dst=/build", "--entrypoint=/usr/bin/gcc", f.backend.Image,
		"-O2", "-Wl,-z,noexecstack", "-o", "/build/program", "/build/managed.c")
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("independent managed compiler probe build failed: complete=%t exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
	return f
}

func captureManagedFixture(t *testing.T, ctx context.Context, f dynamicBoundaryFixture, mode string) (*DynamicProgram, *Envelope) {
	t.Helper()
	args := []string{mode, "argument with spaces"}
	program, err := captureManagedCompilerProgram(ctx, DynamicProgramRequest{Program: f.primary, Args: args, Runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "caller-mutated-mode"
	t.Cleanup(func() {
		if err := program.Close(); err != nil {
			t.Error(err)
		}
	})
	return program, captureFixtureEnvelope(t, f.request)
}

func managedCompilerOutput(t *testing.T, completed bool, exit int, stdout, stderr []byte, err error, fault bool) {
	t.Helper()
	marker, code := "managed-compiler-pass\n", 0
	if fault {
		marker, code = "managed-compiler-failure\n", 9
	}
	text := strings.TrimPrefix(string(stdout), "managed-pkey-unavailable\n")
	if !completed || exit != code || text != marker || len(stderr) != 0 || (err != nil) != fault {
		t.Fatalf("managed compiler healthy/fault parity failed: complete=%t exit=%d error=%v", completed, exit, err)
	}
}

func TestInstalledLinuxManagedCompilerBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := prepareManagedCompilerFixture(t, ctx)
	for _, mode := range []string{"outside", "outside-failure"} {
		out, err := runAdditionalContainer(t, ctx, f, mode, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--tmpfs", "/tmp:rw,mode=1777",
			"--mount", "type=bind,src="+f.primary+",dst=/control,readonly", "--entrypoint=/control", f.backend.Image, mode)
		managedCompilerOutput(t, out.Completed, out.ExitCode, out.Stdout, out.Stderr, err, mode == "outside-failure")
	}
	for _, mode := range []string{"healthy", "failure"} {
		program, envelope := captureManagedFixture(t, ctx, f, mode)
		out, err := RunDynamicProgram(ctx, f.backend, program, envelope)
		managedCompilerOutput(t, out.Completed, out.ExitCode, out.Stdout, out.Stderr, err, mode == "failure")
		assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, envelope)
		assertNoManagedContainers(t, f.docker)
	}
	// Public dynamic capture retains its strict executable-memory restrictions.
	program, envelope := f.capture(t, "strict")
	out, err := RunDynamicProgram(ctx, f.backend, program, envelope)
	managedCompilerOutput(t, out.Completed, out.ExitCode, out.Stdout, out.Stderr, err, false)
	assertNoManagedContainers(t, f.docker)
}

const independentManagedWasm = `
'use strict';
try {
 const bytes=Uint8Array.of(0,97,115,109,1,0,0,0,1,5,1,96,0,1,127,3,2,1,0,7,5,1,1,102,0,0,10,6,1,4,0,65,7,11);
 const value=new WebAssembly.Instance(new WebAssembly.Module(bytes)).exports.f();
 if(value!==(process.argv[2]==='failure'?8:7)){process.stdout.write('managed-wasm-failure\n');process.exitCode=9;}
 else process.stdout.write('managed-wasm-pass\n');
} catch (_) {process.stderr.write('managed-wasm-startup-failed\n');process.exitCode=79;}
`

func TestInstalledLinuxManagedCompilerWasm(t *testing.T) {
	requireManagedFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := prepareIndependentDynamicFixture(t, ctx)
	node := os.Getenv("HAYAKU_DYNAMIC_NODE")
	identity, err := fileDigestContext(ctx, node)
	if err != nil || identity != os.Getenv("HAYAKU_DYNAMIC_NODE_SHA256") {
		t.Fatal("installed Node differs from its explicit official byte identity")
	}
	version, err := runAdditionalContainer(t, ctx, f, "wasm-version", "--read-only", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--entrypoint=/node", f.backend.Image, "--version")
	if err != nil || !version.Completed || version.ExitCode != 0 || string(version.Stdout) != "v22.18.0\n" || len(version.Stderr) != 0 {
		t.Fatal("actual pinned Node version control failed", err)
	}
	resources := filepath.Join(f.directory, "wasm-resources")
	if err := os.Mkdir(resources, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"main.cjs": independentManagedWasm, "openssl.cnf": "# independent managed Wasm configuration\n"} {
		if err := os.WriteFile(filepath.Join(resources, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	libdir, loader := "/lib/aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	if f.backend.Architecture == "amd64" {
		libdir, loader = "/lib/x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	}
	libraries := []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}
	for _, name := range libraries {
		role := "wasm-copy-" + strings.ReplaceAll(name, "+", "p")
		out, err := runAdditionalContainer(t, ctx, f, role, "--mount", "type=bind,src="+f.directory+",dst=/build", "--entrypoint=/bin/cp", f.backend.Image, "-L", libdir+"/"+name, "/build/libs/"+name)
		if err != nil || !out.Completed || out.ExitCode != 0 {
			t.Fatal("installed Node runtime library capture failed", err)
		}
	}
	request := RuntimeRequest{Loader: filepath.Join(f.directory, "libs", loader)}
	for _, name := range libraries {
		request.Libraries = append(request.Libraries, RuntimeLibrary{Name: name, Path: filepath.Join(f.directory, "libs", name)})
	}
	closedRuntime, err := CaptureRuntime(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closedRuntime.Close(); err != nil {
			t.Error(err)
		}
	}()
	envelope := captureFixtureEnvelope(t, EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: resources, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}}, ScratchBytes: 8 << 20})
	for _, mode := range []string{"healthy", "failure"} {
		args := []string{"--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", mode}
		ordinaryArgs := []string{"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534",
			"--mount", "type=bind,src=" + node + ",dst=/node,readonly", "--mount", "type=bind,src=" + resources + ",dst=/resources,readonly", "--entrypoint=/usr/bin/env", f.backend.Image, "-i", "/node"}
		ordinary, ordinaryErr := runAdditionalContainer(t, ctx, f, "wasm-original-"+mode, append(ordinaryArgs, args...)...)
		program, err := captureManagedCompilerProgram(ctx, DynamicProgramRequest{Program: node, Args: args, Runtime: closedRuntime})
		if err != nil {
			t.Fatal(err)
		}
		captured, capturedErr := RunDynamicProgram(ctx, f.backend, program, envelope)
		if err := program.Close(); err != nil {
			t.Error(err)
		}
		marker, exit := "managed-wasm-pass\n", 0
		if mode == "failure" {
			marker, exit = "managed-wasm-failure\n", 9
		}
		for _, control := range []struct {
			completed bool
			exit      int
			stdout    []byte
			stderr    []byte
			err       error
		}{{ordinary.Completed, ordinary.ExitCode, ordinary.Stdout, ordinary.Stderr, ordinaryErr}, {captured.Completed, captured.ExitCode, captured.Stdout, captured.Stderr, capturedErr}} {
			if !control.completed || control.exit != exit || string(control.stdout) != marker || len(control.stderr) != 0 || (control.err != nil) != (mode == "failure") {
				t.Fatalf("ordinary/captured managed Wasm healthy/fault parity failed: complete=%t exit=%d error=%v", control.completed, control.exit, control.err)
			}
		}
		assertManagedEnvelopeNoAuthority(t, f.backend, node, envelope)
		assertNoManagedContainers(t, f.docker)
	}
}

func TestInstalledLinuxManagedCompilerLifecycle(t *testing.T) {
	for _, fault := range []string{"source-bytes", "source-mode", "copied-profile-contract", "cleanup", "cancel", "output-overflow"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			f := prepareManagedCompilerFixture(t, ctx)
			mode := "hold"
			if fault == "cancel" {
				mode = "wait"
			} else if fault == "output-overflow" {
				mode = "overflow"
			}
			program, envelope := captureManagedFixture(t, ctx, f, mode)
			run, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan staticProgramResult, 1)
			go func() {
				output, err := RunDynamicProgram(run, f.backend, program, envelope)
				done <- staticProgramResult{output, err}
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
						t.Error("managed compiler invocation did not join")
					}
				}
				if joined && recoverCleanup != nil {
					recoverCleanup()
				}
			}()
			if fault != "output-overflow" {
				// Reuse the existing independently observed READY protocol and mount
				// inspection, rather than timing mutations against process startup.
				id := waitIndependentDynamicReady(t, ctx, f, done)
				mounts := dynamicBoundaryMounts(t, ctx, f, id)
				switch fault {
				case "source-bytes":
					mutateIndependentDynamicBytes(t, f.primary)
				case "source-mode":
					if err := os.Chmod(f.primary, 0500); err != nil {
						t.Fatal(err)
					}
				case "copied-profile-contract":
					mutateIndependentDynamicBytes(t, filepath.Join(mounts["/input"], "contract.json"))
				case "cleanup":
					root := mounts["/input"]
					if !strings.HasPrefix(filepath.Base(root), "hayaku-native-envelope-") || mounts["/runtime"] == "" {
						t.Fatal("managed cleanup target is not invocation-owned")
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
				case "cancel":
					stop()
				}
			}
			var result staticProgramResult
			select {
			case result = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("managed compiler lifecycle exceeded original deadline")
			}
			if result.err == nil || result.out.Completed {
				t.Fatal("managed compiler lifecycle fault remained complete", fault)
			}
			if fault != "cancel" && fault != "output-overflow" {
				text := strings.TrimPrefix(string(result.out.Stdout), "managed-pkey-unavailable\n")
				if result.out.ExitCode != 0 || text != "independent-dynamic-ready\nindependent-dynamic-done\n" || len(result.out.Stderr) != 0 {
					t.Fatal("managed compiler guard fault did not follow successful actual execution")
				}
			}
			if fault == "cancel" && !errors.Is(result.err, context.Canceled) {
				t.Fatal("managed compiler cancellation was masked", result.err)
			}
			if fault == "copied-profile-contract" && !strings.Contains(result.err.Error(), "native copied control bytes changed") {
				t.Fatal("managed profile control mutation was masked", result.err)
			}
			if fault == "cleanup" && !strings.Contains(result.err.Error(), "cleanup") {
				t.Fatal("managed cleanup fault was masked", result.err)
			}
			if fault == "output-overflow" {
				data := strings.TrimPrefix(string(result.out.Stdout), "managed-pkey-unavailable\n")
				if ctx.Err() != nil || errors.Is(result.err, context.DeadlineExceeded) || len(data) == 0 || len(result.out.Stdout) > 32<<20 || len(result.out.Stderr) != 0 || strings.Trim(data, "x") != "" {
					t.Fatal("managed compiler output overflow was masked or exceeded its framing bound")
				}
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestManagedCompilerProfileIsPrivateAndSealed(t *testing.T) {
	closedRuntime, public, _ := dynamicFixture(t)
	if public.profile != "" {
		t.Fatal("public dynamic capture enabled a trusted compiler profile")
	}
	managed, err := captureManagedCompilerProgram(context.Background(), DynamicProgramRequest{Program: public.primary.source, Args: []string{"fixed"}, Runtime: closedRuntime})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := managed.Close(); err != nil {
			t.Error(err)
		}
	}()
	if managed.profile != managedCompilerProfile || managedCompilerProfile != "trusted-managed-compiler-v1" || managed.Verify(context.Background()) != nil {
		t.Fatal("private compiler profile was not admitted and sealed")
	}
	for _, profile := range []string{"", "caller-forged-profile-v1"} {
		managed.profile = profile
		if managed.Verify(context.Background()) == nil {
			t.Fatal("mutated private compiler profile retained its seal")
		}
	}
	managed.profile = managedCompilerProfile
	if managed.Verify(context.Background()) != nil {
		t.Fatal("restored original compiler profile failed its healthy control")
	}
	public.profile = managedCompilerProfile
	if public.Verify(context.Background()) == nil {
		t.Fatal("public dynamic program was elevated through a forged compiler profile")
	}
}

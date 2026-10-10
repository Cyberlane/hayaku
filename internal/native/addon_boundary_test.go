package native

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

// This fixture is independent native code. Its original execution consumes the
// positive syscall effects before the same exports require permission denials
// inside the modified manual-loader profile. Neither execution issues receipts.
const independentAddonC = `
#define _GNU_SOURCE
#include <node_api.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
static int inside;
static void check(const char *tag,int ok){if(!ok){fprintf(stderr,"addon-independent-failed:%s errno=%d\n",tag,errno);exit(73);}}
static void *thread(void *unused){
 char value[7]={0};int fd=open("data/value",O_RDONLY);check("thread-declared-open",fd>=0);check("thread-declared-bytes",read(fd,value,7)==7&&!memcmp(value,"fixture",7));close(fd);
 fd=open("/etc/os-release",O_RDONLY);if(inside)check("thread-outside-denied",fd<0&&(errno==EPERM||errno==EACCES));else{check("thread-outside-positive",fd>=0&&read(fd,value,1)==1);close(fd);}return NULL;
}
static napi_value probe(napi_env env,napi_callback_info info){
 size_t argc=1;napi_value argv[1];bool confined=false;check("napi-args",napi_get_cb_info(env,info,&argc,argv,NULL,NULL)==napi_ok&&argc==1);check("napi-bool",napi_get_value_bool(env,argv[0],&confined)==napi_ok);inside=confined;
 pthread_t worker;check("addon-pthread-create",pthread_create(&worker,NULL,thread,NULL)==0);check("addon-pthread-join",pthread_join(worker,NULL)==0);
 pid_t child=fork();if(inside)check("addon-fork-denied",child<0&&errno==EPERM);else{check("outside-fork-positive",child>=0);if(!child)_exit(0);int status;check("outside-child-reaped",waitpid(child,&status,0)==child&&WIFEXITED(status)&&WEXITSTATUS(status)==0);}
 int channel=socket(AF_INET,SOCK_STREAM,0);if(inside)check("addon-socket-denied",channel<0&&errno==EPERM);else{check("outside-socket-positive",channel>=0);close(channel);}
 void *memory=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);if(inside)check("addon-anonymous-exec-denied",memory==MAP_FAILED&&errno==EPERM);else{check("outside-anonymous-exec-positive",memory!=MAP_FAILED);munmap(memory,4096);}
 const char *paths[]={"identical.node",inside?"/scratch/identical.node":"/tmp/identical.node"};
 for(int i=0;i<2;i++){int fd=open(paths[i],O_RDONLY);check("identical-elf-read-positive",fd>=0);struct stat before,after;check("identical-stat-positive",fstat(fd,&before)==0&&S_ISREG(before.st_mode));memory=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);if(inside)check("identical-exec-denied",memory==MAP_FAILED&&(errno==EPERM||errno==EACCES));else{check("outside-identical-exec-positive",memory!=MAP_FAILED);munmap(memory,4096);}check("noexec-preserves-metadata",fstat(fd,&after)==0&&before.st_dev==after.st_dev&&before.st_ino==after.st_ino&&before.st_mode==after.st_mode&&before.st_size==after.st_size&&before.st_mtim.tv_sec==after.st_mtim.tv_sec&&before.st_mtim.tv_nsec==after.st_mtim.tv_nsec);close(fd);}
 napi_value result;check("napi-result",napi_create_int32(env,7,&result)==napi_ok);return result;
}
static napi_value initialize(napi_env env,napi_value exports){napi_value fn;check("napi-export",napi_create_function(env,"probe",NAPI_AUTO_LENGTH,probe,NULL,&fn)==napi_ok);check("napi-property",napi_set_named_property(env,exports,"probe",fn)==napi_ok);return exports;}
NAPI_MODULE(NODE_GYP_MODULE_NAME,initialize)
`

const independentAddonNode = `
const fs=require('node:fs');
function check(tag,ok){if(!ok)throw Error('addon-independent-js:'+tag)}
async function main(){
 const mode=process.argv[2],outside=mode==='outside',role=process.argv[3];
 check('arguments',['outside','positive','hold','wait','failure','stdout-limit','stdout-overflow','stderr-limit','stderr-overflow'].includes(mode));
 check('fixed-env',Object.keys(process.env).sort().join(',')==='DECLARED,FIXED'&&process.env.DECLARED==='fixture'&&process.env.FIXED==='second');
 check('declared-bytes',fs.readFileSync('data/value','utf8')==='fixture');
 if(!outside){check('manual-loader-observable',process.argv0==='/runtime/#program'&&process.execPath===process.argv[0]&&process.execPath.startsWith('/runtime/ld-linux-'));check('transport-role',role==='/runtime/#addon-tiny.node');check('empty-stdin',fs.readFileSync(0).length===0);}
 const addon=require(role);check('native-export',typeof addon.probe==='function');
 if(!outside)check('role-mode',(fs.statSync(role).mode&0o777)===0o444);
 const identical=fs.readFileSync('identical.node'),copy=outside?'/tmp/identical.node':'/scratch/identical.node';
 fs.writeFileSync(copy,identical);check('identical-copy-bytes',fs.readFileSync(copy).equals(identical));
 check('native-result',addon.probe(!outside)===7);
 if(!outside){for(const path of ['/resources/identical.node','/scratch/identical.node']){let denied=false;try{require(path)}catch(e){denied=/Permission denied|Operation not permitted|failed to map segment/.test(String(e.message))}check('identical-dlopen-denied',denied);}}
 if(mode==='failure')check('seeded-failure',addon.probe(!outside)===8);
 if(mode==='hold'||mode==='wait'){fs.writeSync(1,'independent-dynamic-ready\n');await new Promise(resolve=>setTimeout(resolve,mode==='hold'?5000:45000));fs.writeSync(1,'independent-dynamic-done\n');return;}
 if(mode.includes('limit')||mode.includes('overflow')){const fd=mode.startsWith('stdout')?1:2,block=Buffer.alloc(65536,0x78);for(let i=0;i<512;i++){let offset=0;while(offset<block.length){const n=fs.writeSync(fd,block,offset,block.length-offset);check('output-progress',n>0);offset+=n;}}if(mode.endsWith('overflow'))fs.writeSync(fd,'x');return;}
 fs.writeSync(1,outside?'addon-outside-pass\n':'addon-inside-pass\n');
}
main().catch(error=>{fs.writeSync(2,String(error.message)+'\n');process.exitCode=9});
`

type independentAddonFixture struct {
	dynamicBoundaryFixture
	declaration      RuntimeRequest
	addon, resources string
}

func addonPrerequisite(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		if os.Getenv("HAYAKU_NATIVE_ADDON_REQUIRED") == "1" {
			t.Fatal("required native addon prerequisite missing:", name)
		}
		t.Skip("explicit native addon prerequisite required: " + name)
	}
	return value
}

func prepareIndependentAddonFixture(t *testing.T, ctx context.Context) independentAddonFixture {
	t.Helper()
	image := addonPrerequisite(t, "HAYAKU_NATIVE_IMAGE")
	node := addonPrerequisite(t, "HAYAKU_DYNAMIC_NODE")
	expected := addonPrerequisite(t, "HAYAKU_DYNAMIC_NODE_SHA256")
	headers := addonPrerequisite(t, "HAYAKU_NATIVE_ADDON_HEADERS")
	digest, err := snapshot.FileDigest(ctx, node, DynamicFileLimit)
	if err != nil || digest != expected || !filepath.IsAbs(headers) {
		t.Fatal("explicit Node/header identity unavailable", err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	directory := t.TempDir()
	worker, err := prepareWorker(ctx, goTool, directory, arch)
	if err != nil {
		t.Fatal(err)
	}
	f := independentAddonFixture{dynamicBoundaryFixture: dynamicBoundaryFixture{backend: Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}, docker: docker, primary: node, directory: directory}, addon: filepath.Join(directory, "tiny.node"), resources: filepath.Join(directory, "resources")}
	if err := os.MkdirAll(filepath.Join(f.resources, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"main.cjs": independentAddonNode, "data/value": "fixture", "openssl.cnf": "# independent captured addon startup configuration\n"} {
		if err := os.WriteFile(filepath.Join(f.resources, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "addon.c"), []byte(independentAddonC), 0644); err != nil {
		t.Fatal(err)
	}
	libdir, loader := "/lib/aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libdir, loader = "/lib/x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	}
	libraries := []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}
	script := "set -eu\ngcc -shared -fPIC -O2 -pthread -Wl,-z,noexecstack -I/headers /build/addon.c -o /build/tiny.node\nmkdir /build/libs\ncp -L " + libdir + "/" + loader + " /build/libs/" + loader + "\n"
	for _, name := range libraries {
		script += "cp -L " + libdir + "/" + name + " /build/libs/" + name + "\n"
	}
	script += "cat /build/tiny.node > /build/resources/identical.node\nchmod 0555 /build/resources/identical.node\nchmod 0755 /build /build/resources /build/resources/data\n"
	out, err := runAdditionalContainer(t, ctx, f.dynamicBoundaryFixture, "addon-build", "--mount", "type=bind,src="+directory+",dst=/build", "--mount", "type=bind,src="+headers+",dst=/headers,readonly", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatal("independent NAPI fixture construction failed", err, out.ExitCode)
	}
	addonDigest, err := snapshot.FileDigest(ctx, f.addon, DynamicFileLimit)
	if err != nil {
		t.Fatal("constructed addon identity unavailable", err)
	}
	resourceDigest, err := snapshot.FileDigest(ctx, filepath.Join(f.resources, "identical.node"), DynamicFileLimit)
	if err != nil || resourceDigest != addonDigest {
		t.Fatal("resource ELF must contain the exact constructed addon bytes", err)
	}
	f.declaration = RuntimeRequest{Loader: filepath.Join(directory, "libs", loader), Addons: []RuntimeAddon{{Name: "tiny", Path: f.addon}}}
	for _, name := range libraries {
		f.declaration.Libraries = append(f.declaration.Libraries, RuntimeLibrary{Name: name, Path: filepath.Join(directory, "libs", name)})
	}
	f.runtime, err = CaptureRuntime(ctx, f.declaration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	f.request = EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: f.resources, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}}, Env: []string{"DECLARED=fixture", "FIXED=second"}, ScratchBytes: 8 << 20}
	if err := f.backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	// Same addon bytes and exact explicit startup profile run through the original
	// Node executable first. Its scratch copy is confined to a fresh exec-capable
	// guest tmpfs; the declared resource inputs remain readonly in both runs.
	out, err = runAdditionalContainer(t, ctx, f.dynamicBoundaryFixture, "addon-original", "--read-only", "--user=65534:65534", "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=16m", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--mount", "type=bind,src="+f.addon+",dst=/tiny.node,readonly", "--mount", "type=bind,src="+f.resources+",dst=/resources,readonly", "--workdir=/resources", "--entrypoint=/usr/bin/env", image, "-i", "DECLARED=fixture", "FIXED=second", "/node", "--no-expose-wasm", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", "outside", "/tiny.node")
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "addon-outside-pass\n" {
		t.Fatal("independent original addon effects unavailable", err, out.ExitCode)
	}
	return f
}

func (f independentAddonFixture) program(t *testing.T, ctx context.Context, mode string, runtime *Runtime) (*DynamicProgram, *Envelope) {
	t.Helper()
	role, err := runtime.AddonPath(ctx, "tiny")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--no-expose-wasm", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", mode, role}
	p, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: f.primary, Args: args, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	args[5] = "caller-modified-addon-role"
	return p, captureFixtureEnvelope(t, f.request)
}

func TestInstalledLinuxAddonIndependentRoles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := prepareIndependentAddonFixture(t, ctx)
	p, e := f.program(t, ctx, "positive", f.runtime)
	for i := 0; i < 2; i++ {
		out, err := RunDynamicProgram(ctx, f.backend, p, e)
		if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "addon-inside-pass\n" {
			// The authored JS catch emits only its fixed assertion tag or a
			// system error message. Keep this fixture diagnostic bounded.
			t.Fatalf("captured addon/thread/noexec positive failed: exit=%d error=%v diagnostic=%q", out.ExitCode, err, out.Stderr[:min(len(out.Stderr), 1024)])
		}
		assertNoManagedContainers(t, f.docker)
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
	for _, name := range []string{"missing", "../tiny", ""} {
		if path, err := f.runtime.AddonPath(ctx, name); err == nil || path != "" {
			t.Fatal("absent/invalid role produced transport path")
		}
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	if path, err := f.runtime.AddonPath(stopped, "tiny"); !errors.Is(err, context.Canceled) || path != "" {
		t.Fatal("canceled role lookup lost typed failure", err)
	}
	omitted := f.declaration
	omitted.Addons = nil
	r, err := CaptureRuntime(ctx, omitted)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	if path, err := r.AddonPath(ctx, "tiny"); err == nil || path != "" {
		t.Fatal("undeclared addon role admitted")
	}
	q, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: f.primary, Args: []string{"--no-expose-wasm", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs", "positive", "/runtime/#addon-tiny.node"}, Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	out, err := RunDynamicProgram(ctx, f.backend, q, e)
	if err == nil || !out.Completed || out.ExitCode != 9 || !strings.Contains(string(out.Stderr), "Cannot find module") {
		t.Fatal("omitted addon must not appear in runtime", err, out.ExitCode)
	}
	// Caller-visible declarations are copied. Private role mutation is not a new
	// admission surface; the original immutable seal must reject it before launch.
	saved := f.runtime.addons["tiny"]
	f.runtime.addons["forged"] = saved
	if err := f.runtime.Verify(ctx); err == nil {
		t.Fatal("forged private addon role verified")
	}
	delete(f.runtime.addons, "forged")
	if err := f.runtime.Verify(ctx); err != nil {
		t.Fatal("restored healthy role failed", err)
	}
	assertNoManagedContainers(t, f.docker)
}

func TestInstalledLinuxAddonIndependentLiveGuards(t *testing.T) {
	for _, fault := range []string{"healthy", "source-bytes", "source-inode", "captured-copy-bytes", "transport-bytes", "transport-mode", "cleanup", "cancel", "cancel-and-copy", "fixed-deadline"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			f := prepareIndependentAddonFixture(t, ctx)
			mode := "hold"
			if strings.HasPrefix(fault, "cancel") || fault == "fixed-deadline" {
				mode = "wait"
			}
			p, e := f.program(t, ctx, mode, f.runtime)
			run, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan staticProgramResult, 1)
			go func() {
				out, err := RunDynamicProgram(run, f.backend, p, e)
				done <- staticProgramResult{out, err}
				close(done)
			}()
			joined := false
			started := time.Now()
			var repair func()
			defer func() {
				stop()
				if !joined {
					select {
					case <-done:
						joined = true
					case <-time.After(50 * time.Second):
						t.Error("addon invocation did not join")
					}
				}
				if joined && repair != nil {
					repair()
				}
			}()
			id := waitIndependentDynamicReady(t, ctx, f.dynamicBoundaryFixture, done)
			mounts := dynamicBoundaryMounts(t, ctx, f.dynamicBoundaryFixture, id)
			if mounts["/runtime"] == "" || mounts["/input"] == "" {
				t.Fatal("actual private transport missing")
			}
			transport := filepath.Join(mounts["/runtime"], "#addon-tiny.node")
			info, err := os.Lstat(transport)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0444 {
				t.Fatal("actual addon role mode incorrect", err)
			}
			switch fault {
			case "source-bytes":
				mutateIndependentDynamicBytes(t, f.addon)
			case "source-inode":
				before, err := os.Lstat(f.addon)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := os.Lstat(filepath.Dir(f.addon))
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(f.addon)
				if err != nil {
					t.Fatal(err)
				}
				replacement := f.addon + ".independent-replacement"
				if err := os.WriteFile(replacement, data, before.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				other, err := os.Lstat(replacement)
				if err != nil || os.SameFile(before, other) || before.Mode() != other.Mode() || before.Size() != other.Size() || !before.ModTime().Equal(other.ModTime()) {
					t.Fatal("independent same-byte inode replacement control failed", err)
				}
				if err := os.Rename(replacement, f.addon); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Dir(f.addon), parent.ModTime(), parent.ModTime()); err != nil {
					t.Fatal(err)
				}
				after, err := os.Lstat(filepath.Dir(f.addon))
				if err != nil || !sameProgramInfo(parent, after) {
					t.Fatal("inode replacement changed captured parent metadata", err)
				}
			case "captured-copy-bytes":
				mutateIndependentDynamicBytes(t, f.runtime.addons["tiny"].copy)
			case "transport-bytes", "cancel-and-copy":
				mutateIndependentDynamicBytes(t, transport)
			case "transport-mode":
				if err := os.Chmod(transport, 0555); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				root := mounts["/input"]
				if !strings.HasPrefix(filepath.Base(root), "hayaku-native-envelope-") {
					t.Fatal("cleanup target is not invocation-owned")
				}
				if err := os.Chmod(mounts["/runtime"], 0500); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				repair = func() {
					os.Chmod(root, 0700)
					os.Chmod(mounts["/runtime"], 0700)
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				}
			}
			if strings.HasPrefix(fault, "cancel") {
				stop()
			}
			var result staticProgramResult
			select {
			case result = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("addon postguard exceeded original context")
			}
			if fault == "healthy" {
				if result.err != nil || !result.out.Completed || result.out.ExitCode != 0 || string(result.out.Stdout) != "independent-dynamic-ready\nindependent-dynamic-done\n" {
					t.Fatal("healthy loaded addon failed", result.err)
				}
			} else {
				if result.err == nil || result.out.Completed {
					t.Fatal("loaded addon drift remained complete", fault)
				}
				if strings.HasPrefix(fault, "cancel") || fault == "fixed-deadline" {
					cause := context.Canceled
					if fault == "fixed-deadline" {
						cause = context.DeadlineExceeded
						if elapsed := time.Since(started); elapsed < 25*time.Second || elapsed > 45*time.Second {
							t.Fatal("fixed operational deadline was not bounded", elapsed)
						}
					}
					if !errors.Is(result.err, cause) {
						t.Fatal("typed addon interruption lost", result.err)
					}
					if string(result.out.Stdout) != "independent-dynamic-ready\n" {
						t.Fatal("interrupted addon did not preserve exact started/incomplete output")
					}
					if fault == "cancel-and-copy" && !strings.Contains(result.err.Error(), "changed") {
						t.Fatal("cancel masked copied addon drift", result.err)
					}
				} else if result.out.ExitCode != 0 || string(result.out.Stdout) != "independent-dynamic-ready\nindependent-dynamic-done\n" {
					t.Fatal("postguard oracle did not follow successful native completion", result.out.ExitCode)
				}
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxAddonIndependentAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := prepareIndependentAddonFixture(t, ctx)
	// The alias and equal-byte alternate are separate task-owned directories;
	// creating them does not alter the already captured original addon parent.
	aliasDir, alternateDir := t.TempDir(), t.TempDir()
	alias, alternate := filepath.Join(aliasDir, "tiny.node"), filepath.Join(alternateDir, "tiny.node")
	info, err := os.Lstat(f.addon)
	if err != nil || info.Size() > 1<<20 {
		t.Fatal("bounded tiny fixture identity unavailable", err)
	}
	data, err := os.ReadFile(f.addon)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alternate, data, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(alternate, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	other, err := os.Lstat(alternate)
	if err != nil || os.SameFile(info, other) || info.Size() != other.Size() || info.Mode() != other.Mode() || !info.ModTime().Equal(other.ModTime()) {
		t.Fatal("equal-byte alias alternate identity differs", err)
	}
	if err := os.Symlink(f.addon, alias); err != nil {
		t.Fatal(err)
	}
	declaration := f.declaration
	declaration.Addons = []RuntimeAddon{{Name: "tiny", Path: alias}}
	r, err := CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	p, e := f.program(t, ctx, "positive", r)
	declaration.Addons[0].Name = "caller-forged"
	declaration.Addons[0].Path = alternate
	out, err := RunDynamicProgram(ctx, f.backend, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "addon-inside-pass\n" {
		t.Fatal("healthy captured alias or copied declaration rejected", err, out.ExitCode)
	}
	parent, err := os.Lstat(aliasDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alternate, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(aliasDir, parent.ModTime(), parent.ModTime()); err != nil {
		t.Fatal(err)
	}
	if path, err := r.AddonPath(ctx, "tiny"); err == nil || path != "" {
		t.Fatal("equal semantic alias retarget remained admitted")
	}
	out, err = RunDynamicProgram(ctx, f.backend, p, e)
	if err == nil || out.Completed || len(out.Stdout) != 0 {
		t.Fatal("retargeted addon executed before source veto", err)
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
	assertNoManagedContainers(t, f.docker)
}

func TestInstalledLinuxAddonIndependentOutputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := prepareIndependentAddonFixture(t, ctx)
	for _, mode := range []string{"failure", "stdout-limit", "stdout-overflow", "stderr-limit", "stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			p, e := f.program(t, ctx, mode, f.runtime)
			out, err := RunDynamicProgram(ctx, f.backend, p, e)
			if mode == "failure" {
				if err == nil || !out.Completed || out.ExitCode != 9 || !strings.Contains(string(out.Stderr), "seeded-failure") {
					t.Fatal("addon runtime failure semantics lost", err, out.ExitCode)
				}
			} else {
				payload := out.Stdout
				other := out.Stderr
				if strings.HasPrefix(mode, "stderr") {
					payload = out.Stderr
					other = out.Stdout
				}
				if len(other) != 0 {
					t.Fatal("addon raw-output case contaminated the opposite stream")
				}
				if len(payload) != 32<<20 {
					t.Fatal("raw addon stream bound differs", mode, len(payload))
				}
				for _, b := range payload {
					if b != 'x' {
						t.Fatal("addon stream framing contaminated raw bytes")
					}
				}
				overflow := strings.HasSuffix(mode, "overflow")
				if overflow {
					if err == nil || out.Completed {
						t.Fatal("addon output overflow remained complete")
					}
				} else if err != nil || !out.Completed || out.ExitCode != 0 {
					t.Fatal("exact addon output limit rejected", mode, err, out.ExitCode)
				}
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

const independentRolldownNode = `
const fs=require('node:fs');function check(tag,ok){if(!ok)throw Error('rolldown-independent:'+tag)}
try{const binding=require(process.argv[2]);check('native-exports',typeof binding.parseSync==='function'&&typeof binding.transformSync==='function');
 const parsed=binding.parseSync('fixture.js','const value = 7;',{});check('parse-errors',Array.isArray(parsed.errors)&&parsed.errors.length===0);const ast=JSON.parse(parsed.program);check('parse-ast',ast.node.type==='Program'&&ast.node.body.length===1&&ast.node.body[0].declarations[0].init.value===7);
 const malformed=binding.parseSync('fault.js','const value = ;',{});check('malformed-independent-control',malformed.errors.length>0);
 const transformed=binding.transformSync('fixture.ts','const value: number = 7;',{});check('transform-errors',Array.isArray(transformed.errors)&&transformed.errors.length===0);check('transform-result',typeof transformed.code==='string'&&!transformed.code.includes(': number')&&/const value\s*=\s*7/.test(transformed.code));
 fs.writeSync(1,'rolldown-native-pass\n');}catch(error){fs.writeSync(2,String(error.message)+'\n');process.exitCode=9}
`

func TestInstalledLinuxAddonIndependentOfficialRolldown(t *testing.T) {
	addon := addonPrerequisite(t, "HAYAKU_NATIVE_ADDON_ROLLDOWN")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	digest, err := snapshot.FileDigest(ctx, addon, DynamicFileLimit)
	if err != nil || digest != "61e64e24a3d0511281d73af01b42f6e6384330550c6b7baa2af2ff74941f871c" {
		t.Fatal("official locked Rolldown1.2.12 bytes differ", err)
	}
	f := prepareIndependentAddonFixture(t, ctx)
	if err := os.WriteFile(filepath.Join(f.resources, "rolldown.cjs"), []byte(independentRolldownNode), 0644); err != nil {
		t.Fatal(err)
	}
	declaration := f.declaration
	declaration.Addons = []RuntimeAddon{{Name: "rolldown", Path: addon}}
	r, err := CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	role, err := r.AddonPath(ctx, "rolldown")
	if err != nil || role != "/runtime/#addon-rolldown.node" {
		t.Fatal("official addon role unavailable", err)
	}
	out, err := runAdditionalContainer(t, ctx, f.dynamicBoundaryFixture, "rolldown-original", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+f.primary+",dst=/node,readonly", "--mount", "type=bind,src="+addon+",dst="+role+",readonly", "--mount", "type=bind,src="+f.resources+",dst=/resources,readonly", "--entrypoint=/usr/bin/env", f.backend.Image, "-i", "/node", "--no-expose-wasm", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/rolldown.cjs", role)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "rolldown-native-pass\n" {
		t.Fatal("official original parse/transform control failed", err, out.ExitCode)
	}
	p, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: f.primary, Args: []string{"--no-expose-wasm", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/rolldown.cjs", role}, Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	e := captureFixtureEnvelope(t, f.request)
	out, err = RunDynamicProgram(ctx, f.backend, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "rolldown-native-pass\n" {
		t.Fatal("captured official parse/transform control failed", err, out.ExitCode)
	}
	assertManagedEnvelopeNoAuthority(t, f.backend, f.primary, e)
	assertNoManagedContainers(t, f.docker)
	if after, err := snapshot.FileDigest(ctx, addon, DynamicFileLimit); err != nil || after != digest {
		t.Fatal("official addon changed during actual controls", err)
	}
}

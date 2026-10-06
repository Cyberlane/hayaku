package native

import (
	"context"
	"debug/macho"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

const darwinBoundaryVersion = "hayaku-darwin-pure-boundary-v1"
const darwinDyldProfile = "/System/Library/Sandbox/Profiles/dyld-support.sb"

// Apple kernel and system loader are trusted platform components. Loader-file
// access, sysctl, clocks and entropy remain observable: this operational profile
// cannot qualify arbitrary code. Only the separate reviewed pure-Go producer
// may establish that user code cannot observe these inputs.
const darwinProfile = `(version 3)
(deny default)
(import "dyld-support.sb")
(allow syscall*)
(allow mach-bootstrap)
(allow system-fcntl)
(allow system-mac-syscall)
(allow sysctl-read)
(allow signal (target self))
(allow process-exec (literal (param "PROGRAM")))
(allow file-read* file-test-existence file-map-executable
  (literal (param "PROGRAM")) (subpath "/usr/lib"))
(allow file-read-metadata file-test-existence (path-ancestors (param "PROGRAM")))
`

// The trusted producer compiles this fixed probe with its captured offline
// compiler. Bind runs it again even when every requested group could reuse.
const darwinProbe = `package main
import("fmt";"os";"os/exec";"syscall")
func main(){
 if len(os.Args)!=2 || os.Args[1]!="--probe" {os.Exit(125)}
 denied:=func(e error)bool{return e==syscall.EPERM || e==syscall.EACCES}
 check:=func(ok bool){if !ok{os.Exit(125)}}
 _,e:=os.ReadFile("/private/etc/passwd");check(os.IsPermission(e))
 _,e=os.Stat("/private/etc/passwd");check(os.IsPermission(e))
 _,e=os.ReadDir("/private/etc");check(os.IsPermission(e))
 e=os.WriteFile("denied-write",[]byte("x"),0600);check(os.IsPermission(e))
 e=exec.Command("/usr/bin/true").Run();check(os.IsPermission(e))
 fd,e:=syscall.Socket(syscall.AF_INET,syscall.SOCK_STREAM,0);check(e==nil)
 e=syscall.Connect(fd,&syscall.SockaddrInet4{Port:1,Addr:[4]byte{127,0,0,1}});syscall.Close(fd);check(denied(e))
 fd,e=syscall.Socket(syscall.AF_UNIX,syscall.SOCK_STREAM,0);check(e==nil)
 e=syscall.Connect(fd,&syscall.SockaddrUnix{Name:"/private/tmp/hayaku-forbidden-socket"});syscall.Close(fd);check(denied(e))
 check(os.Getenv("GOMAXPROCS")=="1" && len(os.Environ())==1)
 fmt.Println("hayaku-darwin-pure-boundary-v1")
}`

func prepareDarwinWorker(ctx context.Context, tool, dir string, env []string) (string, error) {
	source, binary := filepath.Join(dir, "darwin-probe.go"), filepath.Join(dir, "darwin-probe")
	if err := os.WriteFile(source, []byte(darwinProbe), 0600); err != nil {
		return "", err
	}
	_, err := runGo(ctx, tool, dir, env, "build", "-trimpath", "-buildvcs=false", "-o", binary, source)
	return binary, err
}

func (b Backend) inspectDarwin(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" || b.Architecture != runtime.GOARCH || b.Docker != "" || b.Image != "" || b.Sandbox == "" {
		return "", errors.New("native macOS backend context unavailable")
	}
	sandbox, err := filepath.EvalSymlinks(b.Sandbox)
	if err != nil || sandbox != "/usr/bin/sandbox-exec" {
		return "", errors.New("native macOS requires the installed system sandbox")
	}
	identities := []string{contractVersion, darwinBoundaryVersion, digest([]byte(darwinProfile)), observablePolicyDigest(), capabilityPolicyDigest(), b.Architecture, b.Sandbox}
	for index, name := range []string{b.Sandbox, b.Worker, darwinDyldProfile, "/usr/lib/dyld", "/usr/sbin/sysctl", "/System/Library/CoreServices/SystemVersion.plist"} {
		sum, err := fileDigestContext(ctx, name)
		if err != nil {
			return "", errors.New("native macOS platform identity unavailable")
		}
		role := name
		if index == 1 {
			// Private probe directories differ between invocations. Its bytes
			// affect outcomes; its host location is separately sealed in-process.
			role = "enforcement-probe"
		}
		identities = append(identities, role, sum)
	}
	controller, err := passcache.ExecutableDigest(ctx)
	if err != nil {
		return "", err
	}
	// Boot identity prevents receipts crossing a kernel/system-loader restart.
	// This binds the trusted platform context, not bytes of the dyld shared cache.
	platform, err := process.RunEnvironment(ctx, model.Command{Executable: "/usr/sbin/sysctl", Args: []string{"-n", "kern.bootsessionuuid", "kern.osrelease", "kern.osversion", "hw.machine"}}, "", []string{})
	if err != nil || !platform.Completed || len(platform.Stderr) != 0 || len(strings.Fields(string(platform.Stdout))) != 4 {
		return "", errors.New("native macOS kernel context unavailable")
	}
	identities = append(identities, controller, string(platform.Stdout))
	return identity(identities), nil
}

func (b *Backend) bindDarwin(ctx context.Context) error {
	output, err := b.executeDarwin(ctx, Artifact{Program: b.Worker, Args: []string{"--probe"}, Env: []string{"GOMAXPROCS=1"}})
	if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != darwinBoundaryVersion+"\n" || len(output.Stderr) != 0 {
		b.Identity = ""
		return errors.New("native macOS enforcement probe failed")
	}
	if err := b.unchanged(ctx); err != nil {
		b.Identity = ""
		return err
	}
	return nil
}

func (b Backend) executeDarwin(ctx context.Context, a Artifact) (result process.Output, runErr error) {
	if runtime.GOOS != "darwin" || b.Architecture != runtime.GOARCH || b.Sandbox == "" || a.Envelope != nil || validateNativeArguments(a.Args, a.Env) != nil {
		return result, errors.New("unsupported native macOS invocation")
	}
	before, err := fileDigestContext(ctx, a.Program)
	if err != nil {
		return result, err
	}
	data, err := readBounded(a.Program, 64<<20)
	if err != nil || digest(data) != before {
		return result, errors.New("native macOS program changed")
	}
	temporary, err := os.MkdirTemp("", "hayaku-darwin-execution-")
	if err != nil {
		return result, err
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			result.Completed = false
			runErr = errors.Join(runErr, errors.New("native macOS cleanup failed"))
		}
	}()
	canonical, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		return result, err
	}
	temporary = canonical
	program := filepath.Join(temporary, "program")
	if err := os.WriteFile(program, data, 0555); err != nil {
		return result, err
	}
	file, err := macho.Open(program)
	if err != nil {
		return result, errors.New("native macOS requires a matching Mach-O executable")
	}
	cpu, kind := file.Cpu, file.Type
	file.Close()
	if kind != macho.TypeExec || (b.Architecture == "arm64" && cpu != macho.CpuArm64) || (b.Architecture == "amd64" && cpu != macho.CpuAmd64) {
		return result, errors.New("native macOS program platform mismatch")
	}
	result, runErr = process.RunEnvironment(ctx, model.Command{Executable: b.Sandbox, Args: append([]string{"-p", darwinProfile, "-D", "PROGRAM=" + program, program}, a.Args...)}, temporary, a.Env)
	if err := ctx.Err(); err != nil {
		result.Completed = false
		return result, errors.Join(runErr, err)
	}
	current, sourceErr := fileDigestContext(ctx, a.Program)
	copied, copyErr := readBounded(program, 64<<20)
	info, modeErr := os.Lstat(program)
	if sourceErr != nil || current != before || copyErr != nil || digest(copied) != before || modeErr != nil || info.Mode().Perm() != 0555 {
		result.Completed = false
		return result, errors.Join(runErr, ctx.Err(), errors.New("native macOS program changed during execution"))
	}
	return result, runErr
}

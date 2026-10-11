//go:build linux

// This source is embedded in the controller and compiled offline for its target.
// The worker is a platform boundary; it knows nothing about a test framework.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const version = "hayaku-linux-boundary-v7"
const managedCompilerProfile = "trusted-managed-compiler-v1"
const childOutputLimit = 128 << 10
const childTimeout = 30 * time.Second

type childContract struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}
type dynamicInvocation struct {
	Loader          string `json:"loader"`
	ResourcesDigest string `json:"resources_digest"`
	CompilerProfile string `json:"compiler_profile,omitempty"`
}
type invocation struct {
	Args         []string           `json:"args"`
	Env          []string           `json:"env"`
	Envelope     bool               `json:"envelope"`
	ScratchBytes int64              `json:"scratch_bytes"`
	Child        *childContract     `json:"child,omitempty"`
	Dynamic      *dynamicInvocation `json:"dynamic,omitempty"`
}

func fail(err error) { fmt.Fprintln(os.Stderr, "boundary unavailable:", err); os.Exit(125) }
func call(number, a, b, c uintptr) (uintptr, error) {
	r, _, e := syscall.RawSyscall6(number, a, b, c, 0, 0, 0)
	if e != 0 {
		return r, e
	}
	return r, nil
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) != 2 || (os.Args[1] != "/input/program" && os.Args[1] != "/input/helper" && os.Args[1] != "--broker") {
		fail(syscall.EINVAL)
	}
	contractFile, err := os.Open("/input/contract.json")
	if err != nil {
		fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(contractFile, 131073))
	contractFile.Close()
	var contract invocation
	if err != nil || len(data) > 131072 || !utf8.Valid(data) || json.Unmarshal(data, &contract) != nil || !validInvocation(contract) {
		fail(syscall.EINVAL)
	}
	broker := os.Args[1] == "--broker"
	child := os.Args[1] == "/input/helper"
	if (broker || child) && contract.Child == nil {
		fail(syscall.EINVAL)
	}
	if contract.Child != nil && !contract.Envelope {
		fail(syscall.EINVAL)
	}
	if contract.Child != nil && !broker && !child {
		if err := startBroker(); err != nil {
			fail(err)
		}
	}
	if contract.Dynamic != nil {
		if broker || child || contract.Child != nil {
			fail(syscall.EINVAL)
		}
		if err := prepareDynamicResources(contract.Dynamic.ResourcesDigest); err != nil {
			fail(err)
		}
	}
	runtime.LockOSThread()
	if err := restrict(contract, broker, child); err != nil {
		fail(err)
	}
	if broker {
		runBroker(contract)
		return
	}
	start := uintptr(3)
	if contract.Child != nil && !child {
		start = 5
	}
	// Keep the Go runtime poller alive until exec atomically closes inherited
	// descriptors. Immediate close_range races other runtime threads.
	const closeRangeCloexec = 1 << 2
	if _, err := call(436, start, ^uintptr(0), closeRangeCloexec); err != nil {
		fail(err)
	}
	resources := "/input"
	if contract.Envelope {
		resources = "/input/resources"
	}
	if contract.Dynamic != nil {
		resources = "/resources"
	}
	if err := syscall.Chdir(resources); err != nil {
		fail(err)
	}
	program, args, env := "/input/program", contract.Args, contract.Env
	if child {
		if err := emptyInput(); err != nil {
			fail(err)
		}
		program, args, env = "/input/helper", contract.Child.Args, contract.Child.Env
	} else {
		fmt.Fprintln(os.Stderr, version)
	}
	if contract.Dynamic != nil {
		program = "/runtime/" + contract.Dynamic.Loader
		args = append([]string{"--inhibit-cache", "--library-path", "/runtime", "/runtime/#program"}, args...)
	}
	fail(syscall.Exec(program, append([]string{program}, args...), env))
}

func emptyInput() error {
	var fds [2]int
	if err := syscall.Pipe2(fds[:], syscall.O_CLOEXEC); err != nil {
		return err
	}
	syscall.Close(fds[1])
	if fds[0] == 0 {
		_, err := call(syscall.SYS_FCNTL, 0, syscall.F_SETFD, 0)
		return err
	}
	defer syscall.Close(fds[0])
	return syscall.Dup3(fds[0], 0, 0)
}

func validValues(args, env []string) bool {
	if len(args) > 128 || len(env) > 128 {
		return false
	}
	remaining := 65536
	for _, list := range [][]string{args, env} {
		for _, v := range list {
			if len(v) > remaining || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				return false
			}
			remaining -= len(v)
		}
	}
	seen := map[string]bool{}
	for _, entry := range env {
		k, _, ok := strings.Cut(entry, "=")
		if !ok || k == "" || seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}
func validInvocation(c invocation) bool {
	if c.Dynamic != nil {
		if c.Dynamic.CompilerProfile != "" && c.Dynamic.CompilerProfile != managedCompilerProfile {
			return false
		}
		if !c.Envelope || c.Child != nil || !runtimeName(c.Dynamic.Loader) || len(c.Dynamic.ResourcesDigest) != 64 {
			return false
		}
		if _, err := hex.DecodeString(c.Dynamic.ResourcesDigest); err != nil {
			return false
		}
		for _, entry := range c.Env {
			key, _, _ := strings.Cut(entry, "=")
			if strings.HasPrefix(key, "LD_") || key == "GLIBC_TUNABLES" {
				return false
			}
		}
	}
	return validValues(c.Args, c.Env) && c.ScratchBytes >= 0 && c.ScratchBytes <= 64<<20 && (c.Envelope || c.ScratchBytes == 0) && (c.Child == nil || validValues(c.Child.Args, c.Child.Env))
}
func restrict(contract invocation, broker, child bool) error {
	// This restricting OS thread performs every broker fork/exec. Trusted timer
	// and IO goroutines may use other threads; no whole-process TSYNC is claimed.
	abi, err := call(444, 0, 0, 1)
	if err != nil || abi < 3 {
		return errors.New("Landlock ABI 3 required")
	}
	handled := uint64((1 << 15) - 1)
	ruleset, err := call(444, uintptr(unsafe.Pointer(&handled)), 8, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(int(ruleset))
	resources := "/input"
	program := "/input/program"
	rights := uint64(1)
	if contract.Envelope {
		resources = "/input/resources"
		rights |= 4
	}
	if contract.Dynamic != nil {
		resources = "/resources"
		program = "/runtime/" + contract.Dynamic.Loader
	}
	if child {
		program = "/input/helper"
	}
	if err := allowPath(ruleset, resources, 4|8); err != nil {
		return err
	}
	if !broker {
		if err := allowPath(ruleset, program, rights); err != nil {
			return err
		}
	}
	if contract.Dynamic != nil {
		if err := allowPath(ruleset, "/runtime", 1|4|8); err != nil {
			return err
		}
	}
	if broker {
		for _, name := range []string{"/boundary", "/input/helper"} {
			if err := allowPath(ruleset, name, 1|4); err != nil {
				return err
			}
		}
		if err := allowPath(ruleset, "/input/contract.json", 4); err != nil {
			return err
		}
	}
	if contract.ScratchBytes != 0 {
		const scratchRights = 2 | 4 | 8 | 16 | 32 | 128 | 256 | 8192 | 16384
		if err := allowPath(ruleset, "/scratch", scratchRights); err != nil {
			return err
		}
	}
	if _, err = call(syscall.SYS_PRCTL, 38, 1, 0); err != nil {
		return err
	}
	if _, err = call(446, ruleset, 0, 0); err != nil {
		return err
	}
	return filter(broker, contract.Dynamic != nil, contract.Dynamic != nil && contract.Dynamic.CompilerProfile == managedCompilerProfile)
}

// The untrusted main receives only anonymous RUN/reply channels. Installation
// precedes ruleset allocation; closed wrappers cannot later close reused FDs.
func startBroker() error {
	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer requestRead.Close()
	replyRead, replyWrite, err := os.Pipe()
	if err != nil {
		requestWrite.Close()
		return err
	}
	defer replyWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		requestWrite.Close()
		replyRead.Close()
		return err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	cmd := exec.Command("/boundary", "--broker")
	cmd.Env = []string{"GOMAXPROCS=1"}
	cmd.Stdin = os.Stdin
	cmd.ExtraFiles = []*os.File{requestRead, replyWrite, readyWrite}
	if err := cmd.Start(); err != nil {
		requestWrite.Close()
		replyRead.Close()
		return err
	}
	installed := false
	defer func() {
		if !installed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	readyWrite.Close()
	ready := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(readyRead, b[:])
		if err == nil && b[0] != 1 {
			err = syscall.EINVAL
		}
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-time.After(5 * time.Second):
		err = errors.New("broker startup timed out")
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		requestWrite.Close()
		replyRead.Close()
		return err
	}
	// After exec the trusted outer container lifecycle owns broker cleanup. The
	// main cannot execute a broker or helper, but same-UID signals remain possible.
	a, err := syscall.Dup(int(requestWrite.Fd()))
	if err != nil {
		return err
	}
	b, err := syscall.Dup(int(replyRead.Fd()))
	if err != nil {
		syscall.Close(a)
		return err
	}
	requestWrite.Close()
	replyRead.Close()
	requestRead.Close()
	replyWrite.Close()
	readyRead.Close()
	readyWrite.Close()
	if err = syscall.Dup3(a, 3, 0); err != nil {
		return err
	}
	if err = syscall.Dup3(b, 4, 0); err != nil {
		return err
	}
	if a > 4 {
		syscall.Close(a)
	}
	if b > 4 {
		syscall.Close(b)
	}
	installed = true
	return nil
}

type childReply struct {
	Complete bool   `json:"complete"`
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout"`
	Stderr   []byte `json:"stderr"`
}
type limitedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
	cancel   context.CancelFunc
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > childOutputLimit-len(w.data) {
		w.overflow = true
		w.cancel()
		return 0, errors.New("managed output exceeds limit")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
func (w *limitedOutput) result() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data, w.overflow
}
func killChild(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
func runBroker(contract invocation) {
	request := os.NewFile(3, "managed-request")
	reply := os.NewFile(4, "managed-reply")
	ready := os.NewFile(5, "managed-ready")
	if _, err := ready.Write([]byte{1}); err != nil {
		fail(err)
	}
	ready.Close()
	// Request observations are synchronous on the trusted launch thread. This
	// supplies a final parser checkpoint; it is not historical event attestation.
	if err := syscall.SetNonblock(3, true); err != nil {
		fail(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	firstDeadline := time.NewTimer(childTimeout)
	defer firstDeadline.Stop()
	for {
		value, err := readRequest()
		if err == nil {
			if value != 1 {
				fail(errors.New("managed request malformed"))
			}
			break
		}
		if !errors.Is(err, syscall.EAGAIN) {
			fail(errors.New("managed request unavailable"))
		}
		select {
		case <-ticker.C:
		case <-firstDeadline.C:
			fail(errors.New("managed request timed out"))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), childTimeout)
	defer cancel()
	stdout := &limitedOutput{cancel: cancel}
	stderr := &limitedOutput{cancel: cancel}
	cmd := exec.CommandContext(ctx, "/boundary", "/input/helper")
	cmd.Env = []string{"GOMAXPROCS=1"}
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killChild(cmd) }
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		fail(errors.New("managed launch failed"))
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	invalid := false
	var waitErr error
waiting:
	for {
		select {
		case waitErr = <-done:
			break waiting
		case <-ticker.C:
			if _, err := readRequest(); !errors.Is(err, syscall.EAGAIN) {
				invalid = true
				cancel()
				killChild(cmd)
				waitErr = <-done
				break waiting
			}
		case <-ctx.Done():
			killChild(cmd)
			waitErr = <-done
			break waiting
		}
	}
	// No asynchronous reader can have read but failed to queue a bad event.
	// EOF/extra data observed before this completion checkpoint invalidates.
	if _, err := readRequest(); !errors.Is(err, syscall.EAGAIN) {
		invalid = true
	}
	out, overflowOut := stdout.result()
	errout, overflowErr := stderr.result()
	complete := !invalid && ctx.Err() == nil && !overflowOut && !overflowErr && cmd.ProcessState != nil
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) {
			complete = false
		}
	}
	payload, err := json.Marshal(childReply{complete, code, out, errout})
	if err != nil {
		fail(err)
	}
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(payload)))
	// Bound a stopped/non-reading main using a trusted writer timer. Main exit0
	// cannot attest broker completion; the controller always tears down its PID tree.
	written := make(chan error, 1)
	go func() { _, err := reply.Write(append(size[:], payload...)); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			fail(errors.New("managed reply unavailable"))
		}
	case <-time.After(5 * time.Second):
		fail(errors.New("managed reply timed out"))
	}
	if invalid {
		fail(errors.New("managed protocol failed"))
	}
	closeDeadline := time.NewTimer(5 * time.Second)
	defer closeDeadline.Stop()
	for {
		_, err := readRequest()
		if errors.Is(err, io.EOF) {
			break
		}
		if !errors.Is(err, syscall.EAGAIN) {
			fail(errors.New("managed protocol failed"))
		}
		select {
		case <-ticker.C:
		case <-closeDeadline.C:
			fail(errors.New("managed close timed out"))
		}
	}
	request.Close()
	reply.Close()
}

// EAGAIN means no next event at this trusted observation checkpoint.
func readRequest() (byte, error) {
	var b [1]byte
	n, err := syscall.Read(3, b[:])
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return b[0], nil
}

func allowPath(ruleset uintptr, name string, rights uint64) error {
	fd, err := syscall.Open(name, 0x200000|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	// Packed landlock_path_beneath_attr (u64 allowed_access, s32 parent_fd).
	rule := [2]uint64{rights, uint64(fd)}
	_, err = call(445, ruleset, 1, uintptr(unsafe.Pointer(&rule)))
	return err
}
func filter(broker, dynamic, managedCompiler bool) error {
	if managedCompiler && (!dynamic || broker) {
		return syscall.EINVAL
	}
	arch := uint32(0xc00000b7)
	clone := uint32(220)
	denied := []uint32{198, 199, 200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211, 212, 117, 97, 40, 41, 39, 51, 264, 265, 268, 270, 271, 280, 282, 241, 281, 242, 243, 244}
	if runtime.GOARCH == "amd64" {
		arch = 0xc000003e
		clone = 56
		denied = []uint32{41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 57, 58, 101, 165, 166, 155, 161, 272, 308, 321, 310, 311, 303, 304, 298}
	} else if runtime.GOARCH != "arm64" {
		return syscall.ENOSYS
	}
	// clone3 and io_uring can bypass classic clone/filesystem filtering.
	denied = append(denied, 425, 426, 427, 438)
	if !dynamic {
		denied = append(denied, 435)
	}
	if runtime.GOARCH == "amd64" {
		denied = append(denied, 323)
	} // userfaultfd on both supported ABIs.
	if dynamic {
		denied = append(denied, 447, 452, 463, 466, 469) // secretmem FD origin, fchmodat2 and newer attribute mutations
		if runtime.GOARCH == "amd64" {
			denied = append(denied, 90, 91, 92, 93, 94, 132, 188, 189, 190, 197, 198, 199, 235, 260, 261, 268, 280)
		} else {
			denied = append(denied, 5, 6, 7, 14, 15, 16, 52, 53, 54, 55, 88)
		}
	}
	// Personality can make read mappings executable; shmat can admit SHM_EXEC.
	if runtime.GOARCH == "amd64" {
		denied = append(denied, 135, 30)
	} else {
		denied = append(denied, 92, 196)
	}
	mmap, mprotect, pkey, memfd := uint32(222), uint32(226), uint32(288), uint32(279)
	if runtime.GOARCH == "amd64" {
		mmap, mprotect, pkey, memfd = 9, 10, 329, 319
	}
	denied = append(denied, memfd)
	if runtime.GOARCH == "amd64" {
		denied = append(denied, 322)
	} else {
		denied = append(denied, 281)
	}
	f := []syscall.SockFilter{
		{Code: 0x20, K: 4}, {Code: 0x15, Jt: 1, K: arch}, {Code: 0x06, K: 0x80000000},
		{Code: 0x20, K: 0},
		// Reject x32 syscall numbers before matching the native syscall table.
		{Code: 0x45, Jf: 1, K: 0x40000000}, {Code: 0x06, K: 0x80000000},
	}
	if dynamic {
		// FIONBIO changes only the admitted descriptor's nonblocking flag.
		// The int pointee may be zero or nonzero; it is not attested here.
		// Callers may remap an admitted descriptor into 0..2. This does not
		// identify inherited stdio or admit any other ioctl/descriptor origin.
		ioctl := uint32(29)
		if runtime.GOARCH == "amd64" {
			ioctl = 16
		}
		block := []syscall.SockFilter{
			{Code: 0x20, K: 20}, {Code: 0x15, Jt: 1, K: 0}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)},
			{Code: 0x20, K: 16}, {Code: 0x25, Jf: 1, K: 2}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)},
			{Code: 0x20, K: 28}, {Code: 0x15, Jt: 1, K: 0}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)},
			{Code: 0x20, K: 24}, {Code: 0x15, Jt: 1, K: 0x5421}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)},
			{Code: 0x06, K: 0x7fff0000},
		}
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: uint8(len(block)), K: ioctl})
		f = append(f, block...)
		f = append(f, syscall.SockFilter{Code: 0x20, K: 0})
	}
	for _, n := range denied {
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: 1, K: n}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)})
	}
	if dynamic {
		// libc clone3 must fall back to the reviewed classic pthread clone flags.
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: 1, K: 435}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.ENOSYS)})
	}
	for _, n := range []uint32{mmap, mprotect, pkey} {
		block := []syscall.SockFilter{{Code: 0x20, K: 36}, {Code: 0x15, Jt: 1, K: 0}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, {Code: 0x20, K: 32}, {Code: 0x45, Jt: 1, K: 4}, {Code: 0x06, K: 0x7fff0000}}
		if dynamic && n == mmap {
			if managedCompiler {
				// The exact captured compiler/runtime is trusted to generate code.
				// V8 Wasm needs anonymous RWX, including its non-PKU fallback.
				// This is not W^X or syscall-level generated-code provenance.
				block = append(block,
					syscall.SockFilter{Code: 0x20, K: 44}, syscall.SockFilter{Code: 0x15, Jt: 1, K: 0}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)},
					syscall.SockFilter{Code: 0x20, K: 40}, syscall.SockFilter{Code: 0x45, Jf: 1, K: 0x20}, syscall.SockFilter{Code: 0x06, K: 0x7fff0000},
					syscall.SockFilter{Code: 0x20, K: 32})
			}
			// RX only from a file descriptor on immutable runtime or kernel noexec data
			// mounts. A valid FD alone is not executable-byte provenance.
			block = append(block, syscall.SockFilter{Code: 0x45, Jf: 1, K: 2}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x20, K: 44}, syscall.SockFilter{Code: 0x15, Jt: 1, K: 0}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x20, K: 40}, syscall.SockFilter{Code: 0x45, Jf: 1, K: 0x20}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x20, K: 52}, syscall.SockFilter{Code: 0x15, Jt: 1, K: 0}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x20, K: 48}, syscall.SockFilter{Code: 0x15, Jf: 1, K: 0xffffffff}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x06, K: 0x7fff0000})
		} else if managedCompiler {
			// Kernel noexec data mounts still prohibit file-backed executable
			// transitions. Anonymous compiler pages may become executable.
			block = append(block, syscall.SockFilter{Code: 0x06, K: 0x7fff0000})
		} else {
			block = append(block, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)})
		}
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: uint8(len(block)), K: n})
		f = append(f, block...)
		f = append(f, syscall.SockFilter{Code: 0x20, K: 0})
	}
	// Reviewed Go runtime thread flags only, plus fixed os/exec process-clone
	// variants in the trusted broker. Namespace/parent/arbitrary flags are denied.
	flags := []uint32{0x50f00}
	if dynamic {
		flags = append(flags, 0x3d0f00)
	}
	if broker {
		flags = append(flags, 0x4111, 0x5111, 0x4100, 0x5100)
	}
	block := []syscall.SockFilter{{Code: 0x20, K: 20}, {Code: 0x15, Jt: 1, K: 0}, {Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, {Code: 0x20, K: 16}}
	for _, flag := range flags {
		block = append(block, syscall.SockFilter{Code: 0x15, Jf: 1, K: flag}, syscall.SockFilter{Code: 0x06, K: 0x7fff0000})
	}
	block = append(block, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)})
	f = append(f, syscall.SockFilter{Code: 0x15, Jf: uint8(len(block)), K: clone})
	f = append(f, block...)
	f = append(f, syscall.SockFilter{Code: 0x06, K: 0x7fff0000})
	prog := syscall.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, err := call(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&prog)))
	runtime.KeepAlive(f)
	return err
}

// Runtime files are mounted readonly and executable. Data is copied into a
// separate kernel noexec tmpfs before any untrusted code or runtime descriptors
// are admitted. This is a modified loader context, never equivalence proof.
type dynamicResource struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}
type dynamicResources struct {
	Entries []dynamicResource `json:"entries"`
	Absent  []string          `json:"absent"`
	Bytes   int64             `json:"bytes"`
}

func runtimeName(name string) bool {
	if len(name) == 0 || len(name) > 255 || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+-", c)) {
			return false
		}
	}
	return true
}
func resourceName(name string) bool {
	return len(name) > 0 && len(name) <= 4096 && utf8.ValidString(name) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\\x00\r\n:")
}
func resourceParent(root, name string) error {
	for parent := filepath.Dir(name); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, parent))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("resource ancestor is not a real directory")
		}
		if parent == "." {
			return nil
		}
	}
}
func sameResourceInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func resourceFile(entry dynamicResource, destination string) error {
	sourceName := filepath.Join("/input/resources", entry.Path)
	if err := resourceParent("/input/resources", entry.Path); err != nil {
		return err
	}
	before, err := os.Lstat(sourceName)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0444 || before.Size() != entry.Size {
		return errors.New("resource source changed")
	}
	source, err := os.OpenFile(sourceName, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	opened, err := source.Stat()
	if err != nil || !sameResourceInfo(before, opened) {
		source.Close()
		return errors.New("resource source changed")
	}
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		source.Close()
		return err
	}
	hash := sha256.New()
	n, readErr := io.CopyBuffer(io.MultiWriter(target, hash), io.LimitReader(source, entry.Size+1), make([]byte, 32<<10))
	targetClose := target.Close()
	after, statErr := source.Stat()
	sourceClose := source.Close()
	if err := errors.Join(readErr, targetClose, sourceClose); err != nil {
		return err
	}
	pathAfter, pathErr := os.Lstat(sourceName)
	if n != entry.Size || statErr != nil || pathErr != nil || !sameResourceInfo(before, after) || !sameResourceInfo(before, pathAfter) || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return errors.New("resource bytes or identity changed")
	}
	return os.Chmod(destination, 0444)
}
func checkResource(entry dynamicResource, root string) error {
	name := filepath.Join(root, entry.Path)
	if err := resourceParent(root, entry.Path); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("resource special mode")
	}
	switch entry.Kind {
	case "directory":
		if !info.IsDir() || info.Mode().Perm() != 0555 {
			return errors.New("resource directory changed")
		}
	case "symlink":
		link, err := os.Readlink(name)
		if err != nil || info.Mode()&os.ModeSymlink == 0 || link != entry.Link {
			return errors.New("resource link changed")
		}
		resolved, err := filepath.EvalSymlinks(name)
		relative, relErr := filepath.Rel(root, resolved)
		if err != nil || relErr != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return errors.New("resource link escaped")
		}
	case "file":
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0444 || info.Size() != entry.Size {
			return errors.New("resource file changed")
		}
		file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		before, statErr := file.Stat()
		if statErr != nil || !sameResourceInfo(info, before) {
			file.Close()
			return errors.New("resource file changed")
		}
		hash := sha256.New()
		n, readErr := io.CopyBuffer(hash, io.LimitReader(file, entry.Size+1), make([]byte, 32<<10))
		after, afterErr := file.Stat()
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		pathAfter, pathErr := os.Lstat(name)
		if n != entry.Size || afterErr != nil || pathErr != nil || !sameResourceInfo(before, after) || !sameResourceInfo(before, pathAfter) || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return errors.New("resource bytes changed")
		}
	default:
		return syscall.EINVAL
	}
	return nil
}
func prepareDynamicResources(expected string) error {
	var runtimeFS, resourcesFS syscall.Statfs_t
	if err := syscall.Statfs("/runtime", &runtimeFS); err != nil {
		return err
	}
	if err := syscall.Statfs("/resources", &resourcesFS); err != nil {
		return err
	}
	if runtimeFS.Flags&1 == 0 || resourcesFS.Type != 0x1021994 || resourcesFS.Flags&8 == 0 {
		return errors.New("readonly runtime and noexec tmpfs required")
	}
	file, err := os.OpenFile("/input/resources.json", os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	if len(data) > 8<<20 || hex.EncodeToString(hash[:]) != expected || !utf8.Valid(data) {
		return errors.New("resource declaration changed")
	}
	var declaration dynamicResources
	if err := json.Unmarshal(data, &declaration); err != nil {
		return err
	}
	if len(declaration.Entries) == 0 || len(declaration.Entries) > 50000 || len(declaration.Absent) > 50000 || declaration.Bytes < 0 || declaration.Bytes > 64<<20 {
		return syscall.EINVAL
	}
	expectedEntries := map[string]dynamicResource{}
	bytes := int64(0)
	for _, entry := range declaration.Entries {
		if !resourceName(entry.Path) {
			return syscall.EINVAL
		}
		if _, duplicate := expectedEntries[entry.Path]; duplicate {
			return syscall.EINVAL
		}
		switch entry.Kind {
		case "directory":
			if entry.Size != 0 || entry.SHA256 != "" || entry.Link != "" {
				return syscall.EINVAL
			}
		case "file":
			if entry.Size < 0 || entry.Size > (64<<20)-bytes || len(entry.SHA256) != 64 || entry.Link != "" {
				return syscall.EINVAL
			}
			if _, err := hex.DecodeString(entry.SHA256); err != nil {
				return err
			}
			bytes += entry.Size
		case "symlink":
			if entry.Size != 0 || entry.SHA256 != "" || len(entry.Link) == 0 || len(entry.Link) > 4096 || !utf8.ValidString(entry.Link) || filepath.IsAbs(entry.Link) || strings.ContainsAny(entry.Link, "\x00\r\n") {
				return syscall.EINVAL
			}
		default:
			return syscall.EINVAL
		}
		expectedEntries[entry.Path] = entry
	}
	if bytes != declaration.Bytes || expectedEntries["."].Kind != "directory" {
		return syscall.EINVAL
	}
	root, err := os.Open("/resources")
	if err != nil {
		return err
	}
	contents, readErr := root.ReadDir(1)
	closeErr = root.Close()
	if (readErr != nil && readErr != io.EOF) || closeErr != nil || len(contents) != 0 {
		return errors.New("resource tmpfs must start empty")
	}
	// Parent-before-child ordering is generated by the controller, then checked.
	for _, entry := range declaration.Entries {
		destination := filepath.Join("/resources", entry.Path)
		if entry.Kind == "symlink" {
			continue
		}
		if err := resourceParent("/resources", entry.Path); err != nil {
			return err
		}
		if entry.Kind == "directory" {
			if entry.Path != "." {
				if err := os.Mkdir(destination, 0755); err != nil {
					return err
				}
			}
		} else if err := resourceFile(entry, destination); err != nil {
			return err
		}
	}
	for _, entry := range declaration.Entries {
		if entry.Kind == "symlink" {
			if err := resourceParent("/resources", entry.Path); err != nil {
				return err
			}
			if err := os.Symlink(entry.Link, filepath.Join("/resources", entry.Path)); err != nil {
				return err
			}
		}
	}
	for i := len(declaration.Entries) - 1; i >= 0; i-- {
		entry := declaration.Entries[i]
		if entry.Kind == "directory" {
			if err := os.Chmod(filepath.Join("/resources", entry.Path), 0555); err != nil {
				return err
			}
		}
	}
	// Bounded non-following membership walk; every leaf independently rehashed.
	pending := []string{"."}
	seen := 0
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entry, ok := expectedEntries[name]
		if !ok {
			return errors.New("undeclared guest resource")
		}
		seen++
		if seen > len(expectedEntries) {
			return errors.New("resource membership exceeds bounds")
		}
		if err := checkResource(entry, "/resources"); err != nil {
			return err
		}
		if entry.Kind == "directory" {
			directory, err := os.OpenFile(filepath.Join("/resources", name), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			children, readErr := directory.ReadDir(len(expectedEntries) - seen + 1)
			closeErr := directory.Close()
			if (readErr != nil && readErr != io.EOF) || closeErr != nil {
				return errors.Join(readErr, closeErr)
			}
			if len(children) > len(expectedEntries)-seen {
				return errors.New("resource membership exceeds bounds")
			}
			for _, child := range children {
				pending = append(pending, filepath.Join(name, child.Name()))
			}
		}
	}
	if seen != len(expectedEntries) {
		return errors.New("resource membership incomplete")
	}
	for _, name := range declaration.Absent {
		if !resourceName(name) || name == "." {
			return syscall.EINVAL
		}
		components := strings.Split(name, "/")
		for i := 1; i <= len(components); i++ {
			info, err := os.Lstat(filepath.Join("/resources", filepath.Join(components[:i]...)))
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if i == len(components) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("guest resource absence changed")
			}
		}
	}
	return nil
}

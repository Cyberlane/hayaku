//go:build linux

// This source is embedded in the controller and compiled offline for its target.
// The worker is a platform boundary; it knows nothing about a test framework.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const version = "hayaku-linux-boundary-v4"
const childOutputLimit = 128 << 10
const childTimeout = 30 * time.Second

type childContract struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}
type invocation struct {
	Args         []string       `json:"args"`
	Env          []string       `json:"env"`
	Envelope     bool           `json:"envelope"`
	ScratchBytes int64          `json:"scratch_bytes"`
	Child        *childContract `json:"child,omitempty"`
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
	return filter(broker)
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
func filter(broker bool) error {
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
	denied = append(denied, 435, 425, 426, 427, 438)
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
	for _, n := range denied {
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: 1, K: n}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)})
	}
	// A static context never admits executable writable mappings or memfd code.
	for _, n := range []uint32{mmap, mprotect, pkey} {
		f = append(f, syscall.SockFilter{Code: 0x15, Jf: 3, K: n}, syscall.SockFilter{Code: 0x20, K: 32}, syscall.SockFilter{Code: 0x45, Jf: 1, K: 4}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x20, K: 0})
	}
	// Reviewed Go runtime thread flags only, plus fixed os/exec process-clone
	// variants in the trusted broker. Namespace/parent/arbitrary flags are denied.
	flags := []uint32{0x50f00}
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

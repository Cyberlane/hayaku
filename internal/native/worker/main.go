//go:build linux

// This source is embedded in the controller and compiled offline for its target.
// The worker is a platform boundary; it knows nothing about a test framework.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const version = "hayaku-linux-boundary-v1"

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
	if len(os.Args) < 2 || os.Args[1] != "/input/program" {
		fail(syscall.EINVAL)
	}
	var contract struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
	}
	data, err := os.ReadFile("/input/contract.json")
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &contract) != nil {
		fail(syscall.EINVAL)
	}
	if len(contract.Args) > 128 || len(contract.Env) > 128 {
		fail(syscall.EINVAL)
	}
	for _, arg := range contract.Args {
		if strings.ContainsRune(arg, 0) {
			fail(syscall.EINVAL)
		}
	}
	seen := map[string]bool{}
	for _, entry := range contract.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) || seen[key] {
			fail(syscall.EINVAL)
		}
		seen[key] = true
	}
	runtime.LockOSThread()
	// ABI 3 supplies REFER and TRUNCATE restrictions as well as basic file rights.
	abi, err := call(444, 0, 0, 1)
	if err != nil || abi < 3 {
		fail(fmt.Errorf("Landlock ABI 3 required"))
	}
	handled := uint64((1 << 15) - 1)
	ruleset, err := call(444, uintptr(unsafe.Pointer(&handled)), 8, 0)
	if err != nil {
		fail(err)
	}
	defer syscall.Close(int(ruleset))
	fd, err := syscall.Open("/input", 0x200000|syscall.O_CLOEXEC, 0)
	if err != nil {
		fail(err)
	}
	// Packed landlock_path_beneath_attr (u64 allowed_access, s32 parent_fd).
	rule := [2]uint64{4 | 8, uint64(fd)}
	_, err = call(445, ruleset, 1, uintptr(unsafe.Pointer(&rule)))
	syscall.Close(fd)
	if err != nil {
		fail(err)
	}
	fd, err = syscall.Open("/input/program", 0x200000|syscall.O_CLOEXEC, 0)
	if err != nil {
		fail(err)
	}
	rule = [2]uint64{1, uint64(fd)}
	_, err = call(445, ruleset, 1, uintptr(unsafe.Pointer(&rule)))
	syscall.Close(fd)
	if err != nil {
		fail(err)
	}
	if _, err = call(syscall.SYS_PRCTL, 38, 1, 0); err != nil {
		fail(err)
	}
	if _, err = call(446, ruleset, 0, 0); err != nil {
		fail(err)
	}
	if err = filter(); err != nil {
		fail(err)
	}
	// No inherited descriptors, stdin content or ambient guest environment.
	// close_range is required instead of guessing a descriptor upper bound.
	if _, err = call(436, 3, ^uintptr(0), 0); err != nil {
		fail(err)
	}
	if err = syscall.Chdir("/input"); err != nil {
		fail(err)
	}
	fmt.Fprintln(os.Stderr, version)
	err = syscall.Exec("/input/program", append([]string{"/input/program"}, contract.Args...), contract.Env)
	fail(err)
}
func filter() error {
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
	denied = append(denied, 435, 425, 426, 427)
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
	// clone is allowed only for threads. No forked child can outlive cancellation.
	f = append(f, syscall.SockFilter{Code: 0x15, Jf: 3, K: clone}, syscall.SockFilter{Code: 0x20, K: 16}, syscall.SockFilter{Code: 0x45, Jt: 1, K: 0x10000}, syscall.SockFilter{Code: 0x06, K: 0x50000 | uint32(syscall.EPERM)}, syscall.SockFilter{Code: 0x06, K: 0x7fff0000})
	prog := syscall.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, err := call(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&prog)))
	runtime.KeepAlive(f)
	return err
}

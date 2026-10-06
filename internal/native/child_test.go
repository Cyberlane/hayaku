package native

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These bytes describe a static ELF; collection does not promise that arbitrary
// machine code is safe or runnable. Operational acceptance uses a real helper.
func childELFFixture(machine elf.Machine) []byte {
	data := make([]byte, 192)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(data[18:], uint16(machine))
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint64(data[32:], 64)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	binary.LittleEndian.PutUint16(data[56:], 2)
	binary.LittleEndian.PutUint32(data[64:], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(data[68:], uint32(elf.PF_R|elf.PF_X))
	binary.LittleEndian.PutUint64(data[96:], 192)
	binary.LittleEndian.PutUint64(data[104:], 192)
	binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_GNU_STACK))
	binary.LittleEndian.PutUint32(data[124:], uint32(elf.PF_R|elf.PF_W))
	return data
}
func childFixture(t *testing.T) *ChildRequest {
	t.Helper()
	name := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(name, childELFFixture(elf.EM_AARCH64), 0700); err != nil {
		t.Fatal(err)
	}
	return &ChildRequest{Program: name, Args: []string{"fixed", "a b"}, Env: []string{"FIXED=value"}}
}
func TestNativeChildCaptureAndCopy(t *testing.T) {
	request := childFixture(t)
	c, err := captureChild(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Args[0] = "caller mutation"
	request.Env[0] = "OTHER=mutation"
	if c.args[0] != "fixed" || c.env[0] != "FIXED=value" {
		t.Fatal("launch values alias caller")
	}
	if err := c.verifySources(context.Background()); err != nil {
		t.Fatal(err)
	}
	copyName := filepath.Join(t.TempDir(), "helper")
	if err := c.materialize(context.Background(), copyName, "arm64"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(copyName)
	if err != nil || !bytes.Equal(data, childELFFixture(elf.EM_AARCH64)) {
		t.Fatalf("immutable helper bytes: %v", err)
	}
	info, err := os.Lstat(copyName)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("helper executable mode")
	}
	if err := c.materialize(context.Background(), filepath.Join(t.TempDir(), "wrong"), "amd64"); err == nil {
		t.Fatal("platform mismatch accepted")
	}
	if err := os.Chmod(copyName, 0755); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyCopy(copyName); err == nil {
		t.Fatal("copy mode drift accepted")
	}
}
func TestNativeChildSourceAndPrivateDrift(t *testing.T) {
	for _, change := range []string{"source-bytes", "source-mode", "private-bytes", "private-args", "private-env", "private-architecture"} {
		t.Run(change, func(t *testing.T) {
			request := childFixture(t)
			c, err := captureChild(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source-bytes":
				data := childELFFixture(elf.EM_AARCH64)
				data[127] = 1
				if err := os.WriteFile(request.Program, data, 0700); err != nil {
					t.Fatal(err)
				}
			case "source-mode":
				if err := os.Chmod(request.Program, 0600); err != nil {
					t.Fatal(err)
				}
			case "private-bytes":
				c.data[127] = 1
			case "private-args":
				c.args[0] = "changed"
			case "private-env":
				c.env[0] = "FIXED=changed"
			case "private-architecture":
				c.architecture = "amd64"
			}
			if err := c.verifySources(context.Background()); err == nil {
				t.Fatal("changed managed launch admitted")
			}
		})
	}
}
func TestNativeChildMalformedAndDynamicReject(t *testing.T) {
	for _, change := range []string{"relative", "nil-path", "nul", "invalid-utf8", "args-count", "args-bytes", "env-duplicate", "env-nul", "not-elf", "dynamic", "interpreter", "shared", "wrong-machine", "no-code", "rwx-load", "exec-stack", "missing-stack"} {
		t.Run(change, func(t *testing.T) {
			r := childFixture(t)
			data := childELFFixture(elf.EM_AARCH64)
			switch change {
			case "relative":
				r.Program = "relative"
			case "nil-path":
				r.Program = ""
			case "nul":
				r.Program += "\x00"
			case "invalid-utf8":
				r.Args = []string{string([]byte{255})}
			case "args-count":
				r.Args = make([]string, 129)
			case "args-bytes":
				r.Args = []string{strings.Repeat("x", 65537)}
			case "env-duplicate":
				r.Env = []string{"A=1", "A=2"}
			case "env-nul":
				r.Env = []string{"A=\x00"}
			case "not-elf":
				data = []byte("untrusted")
			case "dynamic":
				binary.LittleEndian.PutUint32(data[64:], uint32(elf.PT_DYNAMIC))
			case "interpreter":
				binary.LittleEndian.PutUint32(data[64:], uint32(elf.PT_INTERP))
			case "shared":
				binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_DYN))
			case "wrong-machine":
				binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_RISCV))
			case "rwx-load":
				binary.LittleEndian.PutUint32(data[68:], uint32(elf.PF_R|elf.PF_W|elf.PF_X))
			case "exec-stack":
				binary.LittleEndian.PutUint32(data[124:], uint32(elf.PF_R|elf.PF_W|elf.PF_X))
			case "missing-stack":
				binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_NULL))
			case "no-code":
				binary.LittleEndian.PutUint32(data[68:], uint32(elf.PF_R))
			}
			if strings.HasPrefix(change, "not-") || change == "dynamic" || change == "interpreter" || change == "shared" || change == "wrong-machine" || change == "no-code" || change == "rwx-load" || change == "exec-stack" || change == "missing-stack" {
				if err := os.WriteFile(r.Program, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := captureChild(context.Background(), r); err == nil {
				t.Fatal("unsupported launch admitted")
			}
		})
	}
}

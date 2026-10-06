package native

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

// Only the initial managed launch is fixed. These bounds do not establish
// original-program equivalence, provider completeness, or receipt authority.
const ChildOutputLimit = 128 << 10
const ChildTimeout = 30 * time.Second
const childPolicy = "native-fixed-static-child-v1"

// ChildRequest selects one immutable static Linux helper. A RUN request contains
// no caller-supplied executable, arguments, environment, working directory or IO.
// The helper starts in /input/resources with empty stdin and bounded output.
// Main writes byte 1 (RUN) to FD3, reads FD4 as a little-endian uint32 length
// followed by JSON {complete,exit_code,stdout,stderr}, then closes FD3 and drains
// FD4. Output byte slices use JSON base64. Only one managed launch is admitted.
// EOF/extra input observed at the broker completion checkpoint invalidates the
// reply; subsequent events belong to its bounded close phase.
type ChildRequest struct {
	Program string
	Args    []string
	Env     []string
}

type capturedChild struct {
	files                *inputbundle.Bundle
	data                 []byte
	args, env            []string
	architecture, digest string
}

type childContract struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func captureChild(ctx context.Context, request *ChildRequest) (*capturedChild, error) {
	if request == nil {
		return nil, nil
	}
	if len(request.Program) == 0 || len(request.Program) > 4096 || !filepath.IsAbs(request.Program) || !utf8.ValidString(request.Program) || strings.ContainsAny(request.Program, "\x00\r\n") || validateNativeArguments(request.Args, request.Env) != nil {
		return nil, errors.New("invalid native managed child declaration")
	}
	source := filepath.Clean(request.Program)
	bundle, err := inputbundle.Capture(ctx, inputbundle.Request{
		Roots:  []inputbundle.Root{{Name: "helper", Path: filepath.Dir(source), Mount: "."}},
		Inputs: []inputbundle.Input{{Root: "helper", Path: filepath.Base(source)}},
	})
	if err != nil {
		return nil, err
	}
	data, err := readBounded(source, inputbundle.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	architecture, err := staticChildArchitecture(data)
	if err != nil {
		return nil, err
	}
	match := false
	for _, entry := range bundle.Manifest().Entries {
		if entry.Path == filepath.Base(source) && entry.Kind == "file" && entry.SHA256 == digest(data) {
			match = true
		}
	}
	if !match {
		return nil, errors.New("native managed child changed during capture")
	}
	c := &capturedChild{files: bundle, data: data, args: append([]string(nil), request.Args...), env: append([]string(nil), request.Env...), architecture: architecture}
	c.digest = c.currentDigest()
	if err := c.verifySources(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func staticChildArchitecture(data []byte) (string, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("native managed child must be a static Linux ELF executable")
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC || f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || (f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX) || len(f.Progs) == 0 {
		return "", errors.New("native managed child must be a static Linux ELF executable")
	}
	executable := false
	nonExecutableStack := false
	for _, p := range f.Progs {
		if p.Type == elf.PT_GNU_STACK {
			if p.Flags&elf.PF_X != 0 {
				return "", errors.New("native managed child executable stack is unsupported")
			}
			nonExecutableStack = true
		}
		if p.Type == elf.PT_LOAD && p.Flags&(elf.PF_W|elf.PF_X) == elf.PF_W|elf.PF_X {
			return "", errors.New("native managed child writable executable code is unsupported")
		}
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			return "", errors.New("native managed child dynamic extensions are unsupported")
		}
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 {
			executable = true
		}
	}
	if !executable || !nonExecutableStack {
		return "", errors.New("native managed child requires code and an explicit nonexecutable stack")
	}
	switch f.Machine {
	case elf.EM_AARCH64:
		return "arm64", nil
	case elf.EM_X86_64:
		return "amd64", nil
	}
	return "", errors.New("native managed child architecture is unsupported")
}

func (c *capturedChild) currentDigest() string {
	if c == nil || c.files == nil {
		return ""
	}
	return identity(struct {
		Policy, Files, Bytes, Architecture string
		Args, Env                          []string
		Output                             int
		Timeout                            int64
	}{childPolicy, c.files.Manifest().Digest, digest(c.data), c.architecture, c.args, c.env, ChildOutputLimit, int64(ChildTimeout)})
}
func (c *capturedChild) verifySources(ctx context.Context) error {
	if c == nil || c.files == nil || c.digest == "" || c.currentDigest() != c.digest || validateNativeArguments(c.args, c.env) != nil {
		return errors.New("native managed child identity changed")
	}
	arch, err := staticChildArchitecture(c.data)
	if err != nil || arch != c.architecture {
		return errors.New("native managed child identity changed")
	}
	return c.files.VerifySources(ctx)
}
func (c *capturedChild) materialize(ctx context.Context, name, architecture string) error {
	if err := c.verifySources(ctx); err != nil {
		return err
	}
	if c.architecture != architecture {
		return errors.New("native managed child platform mismatch")
	}
	if err := os.WriteFile(name, c.data, 0555); err != nil {
		return err
	}
	return c.verifyCopy(name)
}
func (c *capturedChild) verifyCopy(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 || info.Mode()&os.ModeType != 0 {
		return errors.New("native managed child copy changed")
	}
	data, err := readBounded(name, inputbundle.MaxFileBytes)
	if err != nil || !bytes.Equal(data, c.data) {
		return errors.New("native managed child copy changed")
	}
	return nil
}
func (c *capturedChild) contract() *childContract {
	if c == nil {
		return nil
	}
	return &childContract{Args: c.args, Env: c.env}
}

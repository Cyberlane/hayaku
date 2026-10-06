package native

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"time"
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
	*capturedExecutable
	args, env []string
	digest    string
}

type childContract struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func captureChild(ctx context.Context, request *ChildRequest) (*capturedChild, error) {
	if request == nil {
		return nil, nil
	}
	if validateNativeArguments(request.Args, request.Env) != nil {
		return nil, errors.New("invalid native managed child declaration")
	}
	executable, err := captureStaticExecutable(ctx, request.Program)
	if err != nil {
		return nil, err
	}
	c := &capturedChild{capturedExecutable: executable, args: append([]string(nil), request.Args...), env: append([]string(nil), request.Env...)}
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
	if c == nil || c.capturedExecutable == nil || c.files == nil {
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
	if c == nil || c.capturedExecutable == nil || c.files == nil || c.digest == "" || c.currentDigest() != c.digest || validateNativeArguments(c.args, c.env) != nil {
		return errors.New("native managed child identity changed")
	}
	if err := c.capturedExecutable.verifySources(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("declared input envelope changed")
	}
	return nil
}
func (c *capturedChild) materialize(ctx context.Context, name, architecture string) error {
	if err := c.verifySources(ctx); err != nil {
		return err
	}
	return c.capturedExecutable.materialize(ctx, name, architecture)
}
func (c *capturedChild) verifyCopy(name string) error {
	if c == nil || c.capturedExecutable == nil || c.capturedExecutable.verifyCopy(name) != nil {
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

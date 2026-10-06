package native

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func programFixture(t *testing.T) ProgramRequest {
	t.Helper()
	name := filepath.Join(t.TempDir(), "input", "program")
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, childELFFixture(elf.EM_AARCH64), 0700); err != nil {
		t.Fatal(err)
	}
	return ProgramRequest{Program: name, Args: []string{"fixed", "with spaces"}}
}

func TestNativeProgramImmutableCaptureAndCopy(t *testing.T) {
	request := programFixture(t)
	p, err := CaptureProgram(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Args[0] = "caller changed args"
	if p.args[0] != "fixed" || p.verifySources(context.Background()) != nil {
		t.Fatal("program aliases caller arguments")
	}
	copy := filepath.Join(t.TempDir(), "program")
	if err := p.executable.materialize(context.Background(), copy, "arm64"); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(copy)
	info, statErr := os.Lstat(copy)
	if err != nil || statErr != nil || !bytes.Equal(actual, childELFFixture(elf.EM_AARCH64)) || info.Mode().Perm() != 0555 {
		t.Fatal("captured program copy differs from authored bytes or executable mode")
	}
	if err := p.executable.materialize(context.Background(), filepath.Join(t.TempDir(), "wrong-platform"), "amd64"); err == nil {
		t.Fatal("platform mismatch admitted")
	}
	if err := os.Chmod(copy, 0755); err != nil {
		t.Fatal(err)
	}
	if p.executable.verifyCopy(copy) == nil {
		t.Fatal("actual copied executable mode drift admitted")
	}
}

func TestNativeProgramRejectsUnsupportedRequests(t *testing.T) {
	for _, fault := range []string{"relative", "utf8", "nul", "args", "args-count", "shared", "dynamic", "interpreter", "exec-stack", "wx"} {
		t.Run(fault, func(t *testing.T) {
			r := programFixture(t)
			data := childELFFixture(elf.EM_AARCH64)
			switch fault {
			case "relative":
				r.Program = "relative"
			case "utf8":
				r.Program = "/invalid\xff"
			case "nul":
				r.Program += "\x00"
			case "args":
				r.Args = []string{"invalid\x00"}
			case "args-count":
				r.Args = make([]string, 129)
			case "shared":
				binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_DYN))
			case "dynamic":
				binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_DYNAMIC))
			case "interpreter":
				binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_INTERP))
			case "exec-stack":
				binary.LittleEndian.PutUint32(data[124:], uint32(elf.PF_R|elf.PF_W|elf.PF_X))
			case "wx":
				binary.LittleEndian.PutUint32(data[68:], uint32(elf.PF_R|elf.PF_W|elf.PF_X))
			}
			if fault != "relative" && fault != "utf8" && fault != "nul" {
				if err := os.WriteFile(r.Program, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := CaptureProgram(context.Background(), r); err == nil {
				t.Fatal("unsupported static program admitted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureProgram(ctx, programFixture(t)); err == nil {
		t.Fatal("canceled capture admitted")
	}
}

func TestNativeProgramSourceAliasAndPrivateDrift(t *testing.T) {
	for _, fault := range []string{"bytes", "replacement", "alias", "private-args", "private-bytes", "private-architecture"} {
		t.Run(fault, func(t *testing.T) {
			r := programFixture(t)
			alias := filepath.Join(t.TempDir(), "selected-directory")
			if err := os.Symlink(filepath.Dir(filepath.Dir(r.Program)), alias); err != nil {
				t.Fatal(err)
			}
			r.Program = filepath.Join(alias, "input", filepath.Base(r.Program))
			p, err := CaptureProgram(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if p.verifySources(context.Background()) != nil {
				t.Fatal("healthy actual directory alias rejected")
			}
			switch fault {
			case "bytes":
				data := childELFFixture(elf.EM_AARCH64)
				data[191] = 1
				if err := os.WriteFile(r.Program, data, 0700); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				before, err := os.Stat(r.Program)
				parent, parentErr := os.Stat(filepath.Dir(r.Program))
				if err != nil || parentErr != nil {
					t.Fatal("healthy replacement preflight unavailable")
				}
				other := programFixture(t)
				if err := os.Chmod(other.Program, before.Mode()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(other.Program, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other.Program, r.Program); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Dir(r.Program), parent.ModTime(), parent.ModTime()); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(r.Program)
				if err != nil || os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("same bytes and metadata replacement control not established")
				}
			case "alias":
				other := programFixture(t)
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(filepath.Dir(other.Program)), alias); err != nil {
					t.Fatal(err)
				}
			case "private-args":
				p.args[0] = "private change"
			case "private-bytes":
				p.executable.data[191] = 1
			case "private-architecture":
				p.executable.architecture = "amd64"
			}
			if p.verifySources(context.Background()) == nil {
				t.Fatal("program source or immutable declaration changed undetected")
			}
		})
	}
}

func TestNativeProgramLaunchVetoBeforeBackend(t *testing.T) {
	p, err := CaptureProgram(context.Background(), programFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := envelopeFixture(t)
	e := captureFixtureEnvelope(t, request)
	b := Backend{Docker: filepath.Join(t.TempDir(), "unavailable-docker"), Architecture: "arm64", Identity: "unavailable-context"}
	if out, err := RunProgram(context.Background(), b, p, nil); err == nil || out.Completed || !strings.Contains(err.Error(), "explicit input envelope") {
		t.Fatal("missing envelope reached backend", err)
	}
	if out, err := RunProgram(context.Background(), b, nil, e); err == nil || out.Completed || !strings.Contains(err.Error(), "program identity") {
		t.Fatal("missing program reached backend", err)
	}
	b.Architecture = "amd64"
	if out, err := RunProgram(context.Background(), b, p, e); err == nil || out.Completed || !strings.Contains(err.Error(), "launch context") {
		t.Fatal("wrong platform reached backend", err)
	}
	b.Architecture, b.Identity = "arm64", ""
	if out, err := RunProgram(context.Background(), b, p, e); err == nil || out.Completed || !strings.Contains(err.Error(), "launch context") {
		t.Fatal("unbound backend reached execution", err)
	}
	for _, platform := range []string{"darwin", "windows", "unsupported"} {
		b.Platform = platform
		if out, err := RunProgram(context.Background(), b, p, e); err == nil || out.Completed || !strings.Contains(err.Error(), "requires a Linux backend") {
			t.Fatal("static ELF reached another platform backend", err)
		}
		if out, err := b.executeCaptured(context.Background(), Artifact{}, p.executable); err == nil || out.Completed || !strings.Contains(err.Error(), "requires a Linux backend") {
			t.Fatal("private static transport reached another platform backend", err)
		}
	}
	b.Platform, b.Sandbox = "linux", "/unexpected-sandbox"
	if out, err := RunProgram(context.Background(), b, p, e); err == nil || out.Completed || !strings.Contains(err.Error(), "requires a Linux backend") {
		t.Fatal("mixed platform fields reached backend", err)
	}
	b.Sandbox = ""
	p.args[0] = "changed"
	if out, err := RunProgram(context.Background(), b, p, e); err == nil || out.Completed || !strings.Contains(err.Error(), "program identity") {
		t.Fatal("mutable declaration reached backend", err)
	}
}

// This optional installed Rust CLI is operational evidence only. Its build/test
// orchestration still requires unsupported children; version output grants no
// native runner, original-semantics or cache qualification.
func TestInstalledLinuxStaticProgram(t *testing.T) {
	image, source := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_STATIC_PROGRAM")
	if image == "" || source == "" {
		t.Skip("installed immutable Linux image and explicit static candidate required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := prepareWorker(ctx, goTool, t.TempDir(), arch)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	b := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := b.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := CaptureProgram(ctx, ProgramRequest{Program: source, Args: []string{"--version"}})
	if err != nil {
		t.Fatal(err)
	}
	if expected := os.Getenv("HAYAKU_STATIC_PROGRAM_SHA256"); expected != "" && digest(p.executable.data) != expected {
		t.Fatal("explicit installed static candidate checksum differs")
	}
	if strings.ContainsAny(source, ",:") {
		t.Fatal("unsupported fixture mount path")
	}
	original, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--entrypoint=/program", "--mount", "type=bind,src="+source+",dst=/program,readonly", image, "--version")
	if err != nil || !original.Completed || original.ExitCode != 0 || !strings.HasPrefix(string(original.Stdout), "cargo-nextest 0.9.146 ") {
		t.Fatal("independent installed Rust static CLI baseline unavailable", err)
	}
	request, _ := envelopeFixture(t)
	request.Env = []string{}
	e := captureFixtureEnvelope(t, request)
	for i := 0; i < 2; i++ {
		actual, err := RunProgram(ctx, b, p, e)
		if err != nil || !actual.Completed || actual.ExitCode != 0 || !bytes.Equal(actual.Stdout, original.Stdout) || len(actual.Stderr) != 0 {
			t.Fatalf("static Rust raw transport differs from original: complete=%t exit=%d error=%v", actual.Completed, actual.ExitCode, err)
		}
	}
	if dynamic := os.Getenv("HAYAKU_DYNAMIC_PROGRAM"); dynamic != "" {
		if _, err := CaptureProgram(ctx, ProgramRequest{Program: dynamic, Args: []string{"--version"}}); err == nil {
			t.Fatal("installed dynamic compiler admitted by static-only boundary")
		}
	}
	assertManagedEnvelopeNoAuthority(t, b, source, e)
	left, err := command(ctx, docker, "ps", "--all", "--filter", "label=hayaku.native-boundary=v1", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(left.Stdout)) != "" {
		t.Fatal("static operational run left a container")
	}
}

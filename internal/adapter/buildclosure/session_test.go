package buildclosure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestBuildHelper(t *testing.T) {
	if os.Getenv("HAYAKU_BUILD_CLOSURE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) != 4 {
		os.Exit(2)
	}
	mode, source, build := args[1], args[2], args[3]
	switch mode {
	case "produce-executable":
		data, err := os.ReadFile(os.Args[0])
		if err != nil || os.WriteFile(filepath.Join(build, "helper"), data, 0700) != nil {
			os.Exit(11)
		}
	case "produce":
		data, err := os.ReadFile(filepath.Join(source, "input"))
		if err != nil || os.WriteFile(filepath.Join(build, "product"), data, 0600) != nil {
			os.Exit(3)
		}
	case "consume-query":
		if _, err := os.ReadFile(filepath.Join(build, "query")); err != nil || os.Remove(filepath.Join(build, "query")) != nil || os.WriteFile(filepath.Join(build, "product"), []byte("same product"), 0600) != nil {
			os.Exit(10)
		}
	case "mutate-source":
		if err := os.WriteFile(filepath.Join(source, "input"), []byte("drift"), 0600); err != nil {
			os.Exit(4)
		}
	case "mutate-build":
		if err := os.WriteFile(filepath.Join(build, "unannounced"), []byte("drift"), 0600); err != nil {
			os.Exit(5)
		}
	case "cancel-drift":
		if os.WriteFile(filepath.Join(source, "input"), []byte("drift"), 0600) != nil || os.WriteFile(filepath.Join(build, "ready"), nil, 0600) != nil {
			os.Exit(6)
		}
		time.Sleep(time.Minute)
	case "fail":
		_ = os.WriteFile(filepath.Join(build, "partial"), []byte("partial"), 0600)
		os.Exit(7)
	case "read":
		if _, err := os.ReadFile(filepath.Join(build, "product")); err != nil {
			os.Exit(8)
		}
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func TestOwnedArtifactAdmissionAndExecution(t *testing.T) {
	s, exe := buildSession(t)
	if _, err := s.RunProducer(t.Context(), "build", helperCommand(exe, "produce-executable")); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.Build(), "helper")
	if err := s.CaptureArtifact(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunProducer(t.Context(), "produce", helperCommand(name, "produce")); err != nil {
		t.Fatal(err)
	}
	if data, err := s.ReadFile(t.Context(), "build", "product", 8); err != nil || string(data) != "original" {
		t.Fatalf("admitted artifact did not execute: %q %v", data, err)
	}
	for _, outside := range []string{exe, filepath.Join(s.Source(), "input")} {
		other, otherExe := buildSession(t)
		if outside == exe {
			outside = otherExe
		} else {
			outside = filepath.Join(other.Source(), "input")
		}
		if err := other.CaptureArtifact(t.Context(), outside); err == nil {
			t.Fatal("artifact admission accepted installed/source input")
		}
	}
}

func buildSession(t *testing.T) (*Session, string) {
	return buildSessionWithSetup(t, "bound setup")
}

func buildSessionWithSetup(t *testing.T, setup string) (*Session, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), Request{Source: source, Revision: "fixture", Environment: []string{"HAYAKU_BUILD_CLOSURE_HELPER=1"}, Tools: []string{exe}, Files: map[string][]byte{"query": []byte(setup)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, exe
}

func helperCommand(exe, mode string) model.Command {
	return model.Command{Executable: exe, Dir: "{build}", Args: []string{"-test.run=^TestBuildHelper$", "--", mode, "{source}", "{build}"}}
}

func TestIndependentBuildViewsAndReadOnlyCollection(t *testing.T) {
	left, exe := buildSession(t)
	right, _ := buildSession(t)
	if left.Source() == right.Source() || left.Build() == right.Build() {
		t.Fatal("independent sessions share a private view")
	}
	for _, s := range []*Session{left, right} {
		cmd := helperCommand(exe, "produce")
		if out, err := s.RunProducer(t.Context(), "build", cmd); err != nil || !out.Completed || out.ExitCode != 0 {
			t.Fatalf("produce: %+v %v", out, err)
		}
		if cmd.Args[3] != "{source}" || cmd.Args[4] != "{build}" {
			t.Fatal("invocation mutated caller arguments")
		}
		if out, err := s.Run(t.Context(), helperCommand(exe, "read")); err != nil || !out.Completed {
			t.Fatalf("collection: %+v %v", out, err)
		}
		records, digest, err := s.Records(t.Context())
		if err != nil || len(records) != 1 || len(digest) != 64 || records[0].ID != "build" || len(records[0].OutputDigest) != 64 {
			t.Fatalf("missing observed producer record: %+v %q %v", records, digest, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Records(t.Context()); err == nil {
			t.Fatal("closed session returned usable records")
		}
	}
}

func TestBuildFailuresNeverReturnUsableRecords(t *testing.T) {
	for _, mode := range []string{"mutate-source", "fail", "mutate-build"} {
		t.Run(mode, func(t *testing.T) {
			s, exe := buildSession(t)
			var err error
			if mode == "mutate-build" {
				_, err = s.Run(t.Context(), helperCommand(exe, mode))
			} else {
				_, err = s.RunProducer(t.Context(), "build", helperCommand(exe, mode))
			}
			if err == nil {
				t.Fatal("failed or mutating invocation accepted")
			}
			if _, _, err := s.Records(t.Context()); err == nil {
				t.Fatal("broken session returned usable records")
			}
		})
	}
}

func TestCancellationRetainsSourceDriftError(t *testing.T) {
	s, exe := buildSession(t)
	build := s.Build()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.RunProducer(ctx, "build", helperCommand(exe, "cancel-drift"))
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("canceled helper and integrity checks did not drain")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	waiting := true
	for waiting {
		select {
		case <-deadline.C:
			t.Fatal("helper never reached real mutation checkpoint")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(build, "ready")); err == nil {
				waiting = false
			}
		}
	}
	cancel()
	var err error
	select {
	case err = <-done:
		done <- err
	case <-time.After(20 * time.Second):
		t.Fatal("canceled helper exceeded bounded join")
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "input") {
		t.Fatalf("lost cancellation or independent drift error: %v", err)
	}
}

func TestProducerBindsConsumedSetupAndPriorGeneration(t *testing.T) {
	left, exe := buildSessionWithSetup(t, "first setup")
	right, _ := buildSessionWithSetup(t, "second setup")
	for _, s := range []*Session{left, right} {
		if _, err := s.RunProducer(t.Context(), "consume", helperCommand(exe, "consume-query")); err != nil {
			t.Fatal(err)
		}
	}
	if left.buildBundle.Manifest().Digest != right.buildBundle.Manifest().Digest {
		t.Fatal("oracle requires identical final products")
	}
	l, ld, err := left.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, rd, err := right.Records(t.Context())
	if err != nil || ld == rd || l[0].InputDigest == r[0].InputDigest {
		t.Fatalf("consumed setup lost from provenance: %v", err)
	}
}

func TestImmutableExternalInputsAndSameByteReplacement(t *testing.T) {
	s, exe := buildSession(t)
	dep := t.TempDir()
	name := filepath.Join(dep, "header")
	if err := os.WriteFile(name, []byte("header"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.BindInputs(t.Context(), inputbundle.Request{Roots: []inputbundle.Root{{Name: "headers", Path: dep, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "headers", Path: "header"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunProducer(t.Context(), "build", helperCommand(exe, "produce")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Stat(dep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, filepath.Join(t.TempDir(), "old-header")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("header"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dep, parent.ModTime(), parent.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterParent, parentErr := os.Stat(dep)
	after, fileErr := os.Stat(name)
	if parentErr != nil || fileErr != nil || !same(parent, afterParent) || os.SameFile(info, after) || info.Size() != after.Size() || info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) {
		t.Fatalf("inode oracle was masked by other metadata drift: %v %v", parentErr, fileErr)
	}
	if err := s.Verify(t.Context()); err == nil {
		t.Fatal("same-byte same-mode same-time inode replacement accepted")
	}
}

func TestToolAliasAndCallerEnvironmentRemainBound(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	alias := filepath.Join(tools, "runner")
	if err := os.Symlink(exe, alias); err != nil {
		t.Fatal(err)
	}
	env := []string{"HAYAKU_BUILD_CLOSURE_HELPER=1"}
	s, err := New(t.Context(), Request{Source: source, Revision: "fixture", Environment: env, Tools: []string{alias}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	env[0] = "HAYAKU_BUILD_CLOSURE_HELPER=0"
	if out, err := s.RunProducer(t.Context(), "build", helperCommand(alias, "produce")); err != nil || !out.Completed {
		t.Fatalf("caller environment escaped its copy: %+v %v", out, err)
	}
	data, err := os.ReadFile(filepath.Join(s.Build(), "product"))
	if err != nil || string(data) != "original" {
		t.Fatalf("positive helper did not execute: %q %v", data, err)
	}
	if err := os.Rename(alias, alias+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, alias); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(t.Context()); err == nil {
		t.Fatal("same-target installed tool alias replacement accepted")
	}
}

func TestExpandedArgumentsRejectBeforeExecution(t *testing.T) {
	s, exe := buildSession(t)
	cmd := helperCommand(exe, "produce")
	cmd.Args = append(cmd.Args, strings.Repeat("{source}", 4096))
	if _, err := s.RunProducer(t.Context(), "oversized", cmd); err == nil || !strings.Contains(err.Error(), "expanded") {
		t.Fatalf("expanded limit not enforced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Build(), "product")); !os.IsNotExist(err) {
		t.Fatalf("rejected invocation executed: %v", err)
	}
}

func TestRenamedPrivateRootCleanupPreservesReplacement(t *testing.T) {
	s, exe := buildSession(t)
	if _, err := s.RunProducer(t.Context(), "build", helperCommand(exe, "produce")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(s.Build())
	renamed := root + "-renamed"
	if err := os.Rename(root, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "foreign")
	if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("root retarget yielded successful cleanup")
	}
	data, err := os.ReadFile(foreign)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("cleanup destroyed replacement: %q %v", data, err)
	}
	for _, name := range []string{"source", "build"} {
		if _, err := os.Stat(filepath.Join(renamed, name)); !os.IsNotExist(err) {
			t.Fatalf("retained original %s still contains captured bytes: %v", name, err)
		}
	}
	// Repair only this fixture's replacement; retained ownership permits retry.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(renamed, root); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("owned cleanup retry did not finish: %v", err)
	}
}

func TestPrivateMetadataBatchBoundsAndCopies(t *testing.T) {
	s, exe := buildSession(t)
	if _, err := s.RunProducer(t.Context(), "build", helperCommand(exe, "produce")); err != nil {
		t.Fatal(err)
	}
	names := []string{"query", "product"}
	data, err := s.ReadFiles(t.Context(), "build", names, 19)
	if err != nil || len(data) != 2 || string(data[0]) != "bound setup" || string(data[1]) != "original" {
		t.Fatalf("healthy exact aggregate batch failed: %q %v", data, err)
	}
	data[0][0] = 'X'
	one, err := s.ReadFile(t.Context(), "build", "query", 11)
	if err != nil || string(one) != "bound setup" || names[0] != "query" {
		t.Fatalf("read bytes or invocation slice escaped its copy: %q %v", one, err)
	}
	manifest, err := s.Manifest(t.Context(), "build")
	if err != nil || len(manifest.Entries) != 3 {
		t.Fatalf("complete metadata manifest missing: %+v %v", manifest, err)
	}
	manifest.Entries[0].Path = "forged"
	again, err := s.Manifest(t.Context(), "build")
	if err != nil || again.Entries[0].Path == "forged" {
		t.Fatalf("manifest mutation changed private observations: %v", err)
	}
	data, err = s.ReadFiles(t.Context(), "build", names, 18)
	if err == nil || data != nil {
		t.Fatalf("aggregate-overflow returned partial metadata: %q %v", data, err)
	}
	if _, _, err := s.Records(t.Context()); err == nil {
		t.Fatal("failed metadata batch retained usable records")
	}
}

func TestMetadataRequestValidationAndCanceledRead(t *testing.T) {
	s, _ := buildSession(t)
	for _, names := range [][]string{{"../input"}, {"query", "query"}, {"/etc/passwd"}, {"."}} {
		if data, err := s.ReadFiles(t.Context(), "build", names, 1024); err == nil || data != nil {
			t.Fatalf("unsafe metadata request accepted: %q %v", data, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if data, err := s.ReadFile(ctx, "build", "query", 1024); data != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata read returned data or lost cause: %q %v", data, err)
	}
	if data, err := s.ReadFile(t.Context(), "source", "input", 8); err != nil || string(data) != "original" {
		t.Fatalf("healthy source reader did not retain capture: %q %v", data, err)
	}
}

func TestMetadataStalePreflightFIFORejectsWithoutWriter(t *testing.T) {
	const probe = "HAYAKU_METADATA_FIFO_PROBE"
	if os.Getenv(probe) == "1" {
		dir := t.TempDir()
		name := filepath.Join(dir, "metadata")
		if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		before, err := r.Lstat("metadata")
		if err != nil {
			t.Fatal(err)
		}
		data, err := readAdmittedMetadata(t.Context(), r, "metadata", before, 3)
		if err != nil || string(data) != "abc" {
			t.Fatalf("regular positive failed: %q %v", data, err)
		}
		fmt.Fprintln(os.Stdout, "metadata-regular-pass")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
		fifo, err := os.Lstat(name)
		if err != nil || fifo.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("actual FIFO replacement missing")
		}
		data, err = readAdmittedMetadata(t.Context(), r, "metadata", before, 3)
		if err == nil || data != nil {
			t.Fatalf("stale regular preflight admitted FIFO: %q %v", data, err)
		}
		fmt.Fprintln(os.Stdout, "metadata-fifo-rejected")
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestMetadataStalePreflightFIFORejectsWithoutWriter$", "-test.count=1", "-test.v"}}, root, []string{probe + "=1", "TMPDIR=" + root}, 2048, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "metadata-regular-pass\n") || !strings.Contains(string(out.Stdout), "metadata-fifo-rejected\n") {
		t.Fatalf("bounded stale-preflight child failed: %+v %v", out, err)
	}
}

type metadataWriter struct {
	buffer bytes.Buffer
	mutate func()
}

func (w *metadataWriter) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if w.mutate != nil {
		mutate := w.mutate
		w.mutate = nil
		mutate()
	}
	return n, err
}

func TestMetadataDriftAfterActualReadRejectsWithinAdmission(t *testing.T) {
	for _, fault := range []string{"growth", "inode", "mode", "mtime", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "metadata")
			original := bytes.Repeat([]byte("x"), 96<<10)
			if err := os.WriteFile(name, original, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			before, err := r.Lstat("metadata")
			if err != nil {
				t.Fatal(err)
			}
			if data, err := readAdmittedMetadata(t.Context(), r, "metadata", before, before.Size()); err != nil || !bytes.Equal(data, original) {
				t.Fatalf("exact healthy bytes failed: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mutated := false
			writer := &metadataWriter{mutate: func() {
				mutated = true
				var err error
				switch fault {
				case "growth":
					err = os.Truncate(name, 1<<20)
				case "inode":
					replacement := filepath.Join(t.TempDir(), "replacement")
					err = os.WriteFile(replacement, original, before.Mode().Perm())
					if err == nil {
						err = os.Chtimes(replacement, before.ModTime(), before.ModTime())
					}
					if err == nil {
						err = os.Rename(replacement, name)
					}
				case "mode":
					err = os.Chmod(name, 0644)
				case "mtime":
					mtime := before.ModTime().Add(time.Hour)
					err = os.Chtimes(name, mtime, mtime)
				case "cancel":
					cancel()
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			count, err := readMetadataTo(ctx, r, "metadata", before, before.Size(), writer)
			if !mutated || writer.buffer.Len() == 0 || count > before.Size()+1 || err == nil {
				t.Fatalf("post-read fault failed actual-byte/limit/error oracle: %t %d %d %v", mutated, writer.buffer.Len(), count, err)
			}
			if fault == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("typed cancellation lost: %v", err)
			}
		})
	}
}

func TestEarlierSealedOutputDriftDuringLaterActualRead(t *testing.T) {
	s, exe := buildSession(t)
	if _, err := s.RunProducer(t.Context(), "build", helperCommand(exe, "produce")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(s.Build())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	before, err := r.Lstat("product")
	if err != nil {
		t.Fatal(err)
	}
	queryInfo, err := r.Lstat("query")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := s.ReadFiles(t.Context(), "build", []string{"query", "product"}, 19); err != nil || len(data) != 2 {
		t.Fatalf("two-file healthy control failed: %v", err)
	}
	mutated := false
	writer := &metadataWriter{mutate: func() {
		mutated = true
		name := filepath.Join(s.Build(), "query")
		if err := os.WriteFile(name, []byte("other setup"), queryInfo.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, queryInfo.ModTime(), queryInfo.ModTime()); err != nil {
			t.Fatal(err)
		}
		changed, err := r.Lstat("query")
		if err != nil || !same(queryInfo, changed) {
			t.Fatalf("byte-only oracle changed metadata: %v", err)
		}
	}}
	count, err := readMetadataTo(t.Context(), r, "product", before, 8, writer)
	if err != nil || count != 8 || writer.buffer.String() != "original" || !mutated {
		t.Fatalf("later healthy actual read missing: %d %q %v", count, writer.buffer.String(), err)
	}
	if err := s.Verify(t.Context()); err == nil {
		t.Fatal("lasting earlier byte-only output drift escaped complete barrier")
	}
}

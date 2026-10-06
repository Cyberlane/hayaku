package producer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
)

// This is an actual structured subprocess, with a deliberately explicit
// environment. Fault modes execute before the parent's post-command barrier.
func TestProducerHelper(t *testing.T) {
	if os.Getenv("HAYAKU_PRODUCER_HELPER") != "1" {
		return
	}
	separator := -1
	for i, argument := range os.Args {
		if argument == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(os.Args) < separator+4 {
		os.Exit(90)
	}
	arguments := os.Args[separator+1:]
	mode, source, output := arguments[0], arguments[1], arguments[2]
	data, err := os.ReadFile(filepath.Join(source, "value"))
	if err != nil {
		os.Exit(91)
	}
	if mode == "copy" && len(arguments) > 3 {
		data, err = os.ReadFile(filepath.Join(arguments[3], "generated"))
		if err != nil {
			os.Exit(92)
		}
	}
	if err := os.WriteFile(filepath.Join(output, "generated"), data, 0600); err != nil {
		os.Exit(93)
	}
	switch mode {
	case "copy":
	case "partial":
		os.Exit(1)
	case "source-write":
		if os.WriteFile(filepath.Join(source, "value"), []byte("changed"), 0600) != nil {
			os.Exit(94)
		}
	case "source-replace":
		target := filepath.Join(source, "value")
		parent, parentErr := os.Stat(source)
		info, err := os.Stat(target)
		if err != nil || parentErr != nil || os.WriteFile(target+".new", data, info.Mode()) != nil || os.Chtimes(target+".new", info.ModTime(), info.ModTime()) != nil || os.Rename(target+".new", target) != nil || os.Chtimes(source, parent.ModTime(), parent.ModTime()) != nil {
			os.Exit(95)
		}
		after, err := os.Stat(target)
		if err != nil || os.SameFile(info, after) || after.Mode() != info.Mode() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || os.WriteFile(filepath.Join(output, "inode-confirmed"), []byte("confirmed"), 0600) != nil {
			os.Exit(99)
		}
	case "escape":
		if os.Symlink(filepath.Join(source, "value"), filepath.Join(output, "outside")) != nil {
			os.Exit(96)
		}
	case "local-link":
		if os.Symlink(filepath.Join(output, "generated"), filepath.Join(output, "link")) != nil {
			os.Exit(96)
		}
	case "mutate-dependency":
		if len(arguments) < 4 || os.WriteFile(filepath.Join(arguments[3], "generated"), []byte("other"), 0600) != nil {
			os.Exit(97)
		}
	case "wait":
		for {
			time.Sleep(time.Second)
		}
	case "overflow":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	default:
		os.Exit(98)
	}
	os.Exit(0)
}

func producerRequest(t *testing.T, mode string) Request {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Request{Source: root, Revision: "base", Environment: []string{"HAYAKU_PRODUCER_HELPER=1"}, Stages: []Stage{{ID: "first", Output: "generated-one", Command: model.Command{Dir: ".", Executable: executable, Args: []string{"-test.run=^TestProducerHelper$", "--", mode, "{source}", "{output}"}}}}}
}

func newProducer(t *testing.T, request Request) *Session {
	t.Helper()
	session, err := New(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func TestIndependentRevisionGeneration(t *testing.T) {
	ctx := context.Background()
	baseRequest := producerRequest(t, "copy")
	candidateRequest := producerRequest(t, "copy")
	candidateRequest.Revision = "candidate"
	if err := os.WriteFile(filepath.Join(candidateRequest.Source, "value"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	base, candidate := newProducer(t, baseRequest), newProducer(t, candidateRequest)
	for _, session := range []*Session{base, candidate} {
		if err := session.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		session *Session
		value   string
	}{{base, "base"}, {candidate, "candidate"}} {
		destination := filepath.Join(t.TempDir(), "consumer")
		if err := item.session.Materialize(ctx, destination); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"value", "generated-one/generated"} {
			data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file)))
			if err != nil || string(data) != item.value {
				t.Fatalf("%s = %q, %v", file, data, err)
			}
		}
		if _, err := os.Stat(filepath.Join(item.session.request.Source, "generated-one")); !os.IsNotExist(err) {
			t.Fatal("generation changed original source", err)
		}
	}
	old, err := base.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := candidate.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if old[0].InputsDigest == current[0].InputsDigest || old[0].OutputDigest == current[0].OutputDigest {
		t.Fatal("revision inputs/outputs were not independent")
	}
}

func TestProducerDependencyIdentityAndRequestCopies(t *testing.T) {
	ctx := context.Background()
	request := producerRequest(t, "copy")
	second := request.Stages[0]
	second.ID, second.Output, second.DependsOn = "second", "generated-two", []string{"first"}
	second.Command.Args = append(append([]string(nil), second.Command.Args...), "{producer:first}")
	request.Stages = append(request.Stages, second)
	session := newProducer(t, request)
	request.Stages[0].Command.Args[2] = "partial"
	request.Stages[1].DependsOn[0] = "foreign"
	request.Environment[0] = "HAYAKU_PRODUCER_HELPER=0"
	if err := session.Run(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := session.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].DependsOn[0] != "first" || records[0].InvocationDigest == records[1].InvocationDigest {
		t.Fatal("dependency identity missing", records)
	}
	records[1].DependsOn[0] = "foreign"
	again, err := session.Records(ctx)
	if err != nil || again[1].DependsOn[0] != "first" {
		t.Fatal("records exposed mutable state", err)
	}
	destination := filepath.Join(t.TempDir(), "consumer")
	if err := session.Materialize(ctx, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "generated-two/generated"))
	if err != nil || string(data) != "base" {
		t.Fatal("dependency output missing", err)
	}
}

func TestProducerCommandFaultsInvalidateOutputs(t *testing.T) {
	for _, mode := range []string{"partial", "source-write", "source-replace", "escape", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			session := newProducer(t, producerRequest(t, mode))
			if err := session.Run(context.Background()); err == nil {
				t.Fatal("fault accepted")
			}
			assertProducerReached(t, session)
			if mode == "source-replace" {
				if _, err := os.Stat(filepath.Join(session.private, "output-first", "inode-confirmed")); err != nil {
					t.Fatal("replacement helper did not isolate inode drift", err)
				}
			}
			if _, err := session.Records(context.Background()); err == nil {
				t.Fatal("partial output records published")
			}
			if err := session.Run(context.Background()); err == nil {
				t.Fatal("failed session ran again")
			}
			if err := session.Materialize(context.Background(), filepath.Join(t.TempDir(), "consumer")); err == nil {
				t.Fatal("partial output materialized")
			}
		})
	}
}

func TestProducerCancellationIsStickyAndCleans(t *testing.T) {
	session := newProducer(t, producerRequest(t, "wait"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline was lost", err)
	}
	assertProducerReached(t, session)
	if _, err := session.Records(context.Background()); err == nil {
		t.Fatal("cancelled output published")
	}
	private := session.private
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatal("private generation tree leaked", err)
	}
}

func TestProducerEarlierOutputMutationRejectsDependent(t *testing.T) {
	request := producerRequest(t, "copy")
	second := request.Stages[0]
	second.ID, second.Output, second.DependsOn = "second", "generated-two", []string{"first"}
	second.Command.Args = append([]string(nil), second.Command.Args...)
	second.Command.Args[2] = "mutate-dependency"
	second.Command.Args = append(second.Command.Args, "{producer:first}")
	request.Stages = append(request.Stages, second)
	session := newProducer(t, request)
	if err := session.Run(context.Background()); err == nil {
		t.Fatal("dependency output mutation accepted")
	}
	if _, err := session.Records(context.Background()); err == nil {
		t.Fatal("failed dependency graph published")
	}
}

func TestProducerPhysicalSourceAndToolAliasDrift(t *testing.T) {
	for _, mode := range []string{"source", "tool"} {
		t.Run(mode, func(t *testing.T) {
			request := producerRequest(t, "copy")
			if mode == "tool" {
				alias := filepath.Join(t.TempDir(), "tool")
				if err := os.Symlink(request.Stages[0].Command.Executable, alias); err != nil {
					t.Fatal(err)
				}
				request.Stages[0].Command.Executable = alias
			}
			session := newProducer(t, request)
			if mode == "source" {
				target := filepath.Join(request.Source, "value")
				parent, err := os.Stat(request.Source)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target+".new", []byte("base"), info.Mode()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(target+".new", info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(target+".new", target); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(request.Source, parent.ModTime(), parent.ModTime()); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(target)
				if err != nil || os.SameFile(info, after) || after.Mode() != info.Mode() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
					t.Fatal("replacement oracle did not isolate inode drift", err)
				}
			} else {
				alias := request.Stages[0].Command.Executable
				if err := os.Symlink(session.tools[0].physical.name, alias+".new"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(alias+".new", alias); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Run(context.Background()); err == nil {
				t.Fatal("equal semantic replacement accepted")
			}
		})
	}
}

func assertProducerReached(t *testing.T, session *Session) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(session.private, "output-first", "generated"))
	if err != nil || string(data) != "base" {
		t.Fatalf("fault helper did not reach known output: %q, %v", data, err)
	}
}

func TestProducerSealedOutputDrift(t *testing.T) {
	for _, mode := range []string{"bytes", "mode", "membership"} {
		t.Run(mode, func(t *testing.T) {
			session := newProducer(t, producerRequest(t, "copy"))
			if err := session.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(session.outputRoots[0], "generated")
			switch mode {
			case "bytes":
				if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			case "membership":
				if err := os.WriteFile(target+".extra", []byte("extra"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := session.Records(context.Background()); err == nil {
				t.Fatal("changed sealed output accepted")
			}
			if err := session.Materialize(context.Background(), filepath.Join(t.TempDir(), "consumer")); err == nil {
				t.Fatal("changed sealed output materialized")
			}
		})
	}
}

func TestProducerOutputLocalLinkRolesRemainStable(t *testing.T) {
	ctx := context.Background()
	request := producerRequest(t, "local-link")
	first, second := newProducer(t, request), newProducer(t, request)
	for _, session := range []*Session{first, second} {
		if err := session.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a, err := first.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a[0].OutputDigest != b[0].OutputDigest || a[0].InvocationDigest != b[0].InvocationDigest {
		t.Fatal("owned output paths leaked into stable identities")
	}
	x, err := first.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	y, err := second.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(x) != len(y) {
		t.Fatal("owned output inventory differs")
	}
	for i := range x {
		if x[i] != y[i] {
			t.Fatal("owned output link roles differ")
		}
	}
	destination := filepath.Join(t.TempDir(), "consumer")
	if err := first.Materialize(ctx, destination); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(destination, "generated-one", "link"))
	if err != nil || link != "generated" {
		t.Fatal("output link was not materialized into its captured role", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "generated-one", "link"))
	if err != nil || string(data) != "base" {
		t.Fatal("bound local link did not read expected bytes", err)
	}
	if err := os.Remove(filepath.Join(second.outputRoots[0], "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(request.Source, filepath.Join(second.outputRoots[0], "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Records(ctx); err == nil {
		t.Fatal("retargeted output link accepted")
	}
}

func TestProducerMaterializationAdmissionAndStickyFailure(t *testing.T) {
	ctx := context.Background()
	request := producerRequest(t, "copy")
	session := newProducer(t, request)
	if err := session.Run(ctx); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(request.Source, "consumer")
	if err := session.Materialize(ctx, inside); err == nil {
		t.Fatal("destination inside original source accepted")
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatal("rejected destination modified source", err)
	}
	destination := t.TempDir()
	marker := filepath.Join(destination, "marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := session.Materialize(ctx, destination); err == nil {
		t.Fatal("existing destination overwritten")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "preserve" {
		t.Fatal("unowned destination removed", err)
	}
	if _, err := session.Records(ctx); err == nil {
		t.Fatal("failed materialization did not invalidate session")
	}
}

func TestProducerAdmissionBounds(t *testing.T) {
	for _, mode := range []string{"utf8", "environment", "cycle", "overlap", "original-output", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			request := producerRequest(t, "copy")
			switch mode {
			case "utf8":
				request.Revision = string([]byte{255})
			case "environment":
				request.Environment = []string{"A=1", "A=2"}
			case "cycle":
				request.Stages[0].DependsOn = []string{"first"}
			case "overlap":
				second := request.Stages[0]
				second.ID = "second"
				second.Output += "/nested"
				request.Stages = append(request.Stages, second)
			case "original-output":
				if err := os.Mkdir(filepath.Join(request.Source, request.Stages[0].Output), 0700); err != nil {
					t.Fatal(err)
				}
			case "aggregate":
				request.Limits = inputbundle.Limits{Bytes: 1}
			}
			if session, err := New(context.Background(), request); err == nil {
				_ = session.Close()
				t.Fatal("invalid request accepted")
			}
		})
	}
}

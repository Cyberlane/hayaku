package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/process"
)

func providerLaunchFixture(t *testing.T) (Backend, Artifact, string, *bool) {
	t.Helper()
	b, a, _, decoded := syntheticAuthorityFixture(t)
	root := t.TempDir()
	write(t, root, "input.txt", "closed input")
	e, err := CaptureEnvelope(context.Background(), EnvelopeRequest{Files: inputbundle.Request{
		Roots:  []inputbundle.Root{{Name: "source", Path: root, Mount: "source"}},
		Inputs: []inputbundle.Input{{Root: "source", Path: "."}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a.Envelope = e
	a.authority.launch = &providerLaunch{context: identity("stable reviewed functional context"), envelope: e,
		execute: func(context.Context) (process.Output, error) { return process.Output{Completed: true}, nil }}
	program, err := fileDigest(a.Program)
	if err != nil {
		t.Fatal(err)
	}
	a.authority.binding, err = artifactBinding(b, a, program, a.authority.producer)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyAuthority(context.Background(), b, a); err != nil {
		t.Fatal("healthy private launch admission unavailable", err)
	}
	return b, a, root, decoded
}

func TestProviderLaunchRejectsMutationBeforeCache(t *testing.T) {
	for _, mutation := range []string{"public-copy", "missing-launch", "missing-envelope", "substitute-envelope", "missing-executor", "invalid-context", "changed-context", "source-drift"} {
		for _, populated := range []bool{false, true} {
			for _, audit := range []bool{false, true} {
				t.Run(mutation+map[bool]string{false: "/absent", true: "/populated"}[populated]+map[bool]string{false: "/normal", true: "/audit"}[audit], func(t *testing.T) {
					b, a, root, decoded := providerLaunchFixture(t)
					switch mutation {
					case "public-copy":
						a.authority = nil
					case "missing-launch":
						a.authority.launch = nil
					case "missing-envelope":
						a.Envelope = nil
					case "substitute-envelope":
						other := *a.Envelope
						a.Envelope = &other
					case "missing-executor":
						a.authority.launch.execute = nil
					case "invalid-context":
						a.authority.launch.context = "unqualified"
					case "changed-context":
						a.authority.launch.context = identity("different functional context")
					case "source-drift":
						write(t, root, "input.txt", "changed input")
					}
					if _, _, err := verifyAuthority(context.Background(), b, a); err == nil {
						t.Fatal("mutated private launch retained authority")
					}
					dir, before := authorityCacheFixture(t, populated)
					assertAuthorityRejectedWithoutMutation(t, b, a, dir, before, audit, decoded)
				})
			}
		}
	}
}

func TestProviderLaunchStableKeyAndFreshPhysicalGuards(t *testing.T) {
	b, first, firstRoot, _ := providerLaunchFixture(t)
	_, second, secondRoot, _ := providerLaunchFixture(t)
	// Program location and source roots are invocation guards; their identical
	// bytes and independently sealed functional context determine the key.
	key1, _, err := verifyAuthority(context.Background(), b, first)
	if err != nil {
		t.Fatal(err)
	}
	key2, _, err := verifyAuthority(context.Background(), b, second)
	if err != nil || key1 != key2 || first.Envelope.Digest() == second.Envelope.Digest() || firstRoot == secondRoot {
		t.Fatal("stable context mixed physical locations into receipt key", err)
	}
	if err := os.Remove(filepath.Join(secondRoot, "input.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyAuthority(context.Background(), b, second); err == nil {
		t.Fatal("fresh physical input guard did not reject deletion")
	}
	if _, _, err := verifyAuthority(context.Background(), b, first); err != nil {
		t.Fatal("independent live source unexpectedly invalidated", err)
	}
}

func TestProviderLaunchUsesOnlyOwnedExecutor(t *testing.T) {
	b, a, _, _ := providerLaunchFixture(t)
	want := errors.New("owned launch failure")
	called := false
	a.authority.launch.execute = func(ctx context.Context) (process.Output, error) {
		called = true
		if err := ctx.Err(); err != nil {
			return process.Output{}, err
		}
		return process.Output{Completed: true, ExitCode: 1}, want
	}
	got, err := executeArtifact(context.Background(), b, a)
	if !called || !got.Completed || got.ExitCode != 1 || !errors.Is(err, want) {
		t.Fatal("private launch was replaced or its failure discarded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := verifyAuthority(ctx, b, a); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled launch retained authority", err)
	}
}

func TestNativeExecutionCompletionRejectsOperationalSuccess(t *testing.T) {
	operational := errors.New("private launch cleanup failed")
	for _, tt := range []struct {
		name      string
		output    process.Output
		err       error
		completed bool
	}{
		{"healthy", process.Output{Completed: true}, nil, true},
		{"assertion-failure", process.Output{Completed: true, ExitCode: 1}, errors.New("exit status 1"), true},
		{"cleanup-failure", process.Output{Completed: true}, operational, false},
		{"incomplete", process.Output{}, nil, false},
		{"cancelled", process.Output{}, context.Canceled, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeExecutionCompleted(tt.output, tt.err); got != tt.completed {
				t.Fatal("incorrect execution completion admission")
			}
		})
	}
}

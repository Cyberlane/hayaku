package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

// This fixture constructs a seal only inside the native test package. It tests
// admission mutations, supplies no language proof, and never executes its bytes.
func syntheticAuthorityFixture(t *testing.T) (Backend, Artifact, *error, *bool) {
	t.Helper()
	program := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(program, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	b := Backend{Docker: "/unavailable-docker", Image: "sha256:" + strings.Repeat("a", 64), Worker: "/unavailable-worker", Architecture: "arm64", Identity: strings.Repeat("b", 64)}
	a := Artifact{Name: "unit", Program: program, Proof: strings.Repeat("c", 64), Cases: []string{"TestAlpha", "TestBeta"}, Args: []string{"-test.v", "-test.count=1"}, Env: []string{"GOMAXPROCS=1"}}
	const programDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	producer := strings.Repeat("d", 64)
	binding, err := artifactBinding(b, a, programDigest, producer)
	if err != nil {
		t.Fatal(err)
	}
	var liveErr error
	decoded := false
	a.authority = &artifactAuthority{
		binding: binding, location: authorityLocation(b, a), producer: producer,
		verify: func(ctx context.Context) error {
			if liveErr != nil {
				return liveErr
			}
			return ctx.Err()
		},
		decode: func(process.Output, Artifact) (model.Outcomes, error) {
			decoded = true
			return model.Outcomes{}, nil
		},
	}
	key, programHash, err := verifyAuthority(context.Background(), b, a)
	if err != nil || key != binding || programHash != programDigest {
		t.Fatal("healthy synthetic admission control failed")
	}
	return b, a, &liveErr, &decoded
}

func authorityCacheFixture(t *testing.T, populated bool) (string, map[string][32]byte) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	if !populated {
		return dir, nil
	}
	cache, err := passcache.Open(dir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{strings.Repeat("e", 64), strings.Repeat("f", 64)} {
		prior := receipt{Schema: 1, Key: key, Cases: []string{"TestAlpha", "TestBeta"}, Outcomes: model.Outcomes{
			Complete: true, Units: []model.Outcome{{Unit: "unit", Action: "pass"}},
			Tests: []model.Outcome{{Unit: "unit", Test: "TestAlpha", Action: "pass"}, {Unit: "unit", Test: "TestBeta", Action: "pass"}},
		}}
		if err := cache.Store(context.Background(), key, prior); err != nil {
			t.Fatal(err)
		}
		if _, status := cache.Load(key); status != "hit" {
			t.Fatal("authenticated cache control unavailable")
		}
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, observableCacheSnapshot(t, dir)
}

func assertAuthorityRejectedWithoutMutation(t *testing.T, b Backend, a Artifact, dir string, before map[string][32]byte, audit bool, producerDecoded *bool) {
	t.Helper()
	callerDecoded := false
	result, err := Execute(context.Background(), b, a, dir, audit, func(process.Output, Artifact) (model.Outcomes, error) {
		callerDecoded = true
		return model.Outcomes{}, nil
	})
	if err == nil || result.Qualified || result.Complete || result.Passed || result.Executed || result.Reused || result.Key != "" || callerDecoded || (producerDecoded != nil && *producerDecoded) {
		t.Fatalf("rejected request reached execution or authority: qualified=%t complete=%t passed=%t executed=%t reused=%t keyed=%t decoded=%t", result.Qualified, result.Complete, result.Passed, result.Executed, result.Reused, result.Key != "", callerDecoded || (producerDecoded != nil && *producerDecoded))
	}
	if before == nil {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rejected request created a cache domain")
		}
	} else if !reflect.DeepEqual(before, observableCacheSnapshot(t, dir)) {
		t.Fatal("rejected request changed authenticated domain bytes")
	}
}

func TestNativeAuthorityPublicClaimsCannotOpenOrMutateCache(t *testing.T) {
	for _, claim := range []string{"forged-proof", "public-field-copy", "json-roundtrip", "json-qualified-claim"} {
		for _, populated := range []bool{false, true} {
			for _, audit := range []bool{false, true} {
				name := claim + map[bool]string{false: "/absent", true: "/populated"}[populated] + map[bool]string{false: "/normal", true: "/audit"}[audit]
				t.Run(name, func(t *testing.T) {
					b, admitted, _, decoded := syntheticAuthorityFixture(t)
					var a Artifact
					switch claim {
					case "forged-proof":
						a = Artifact{Name: "unit", Program: admitted.Program, Proof: strings.Repeat("1", 64), Cases: []string{"TestAlpha", "TestBeta"}}
					case "public-field-copy":
						a = Artifact{Name: admitted.Name, Program: admitted.Program, Proof: admitted.Proof, Cases: append([]string(nil), admitted.Cases...), Args: append([]string(nil), admitted.Args...), Env: append([]string(nil), admitted.Env...), Effects: append([]model.Effect(nil), admitted.Effects...)}
					case "json-roundtrip", "json-qualified-claim":
						data, err := json.Marshal(admitted)
						if err != nil {
							t.Fatal(err)
						}
						if claim == "json-qualified-claim" {
							var fields map[string]any
							if err := json.Unmarshal(data, &fields); err != nil {
								t.Fatal(err)
							}
							fields["Qualified"], fields["qualified"] = true, true
							fields["authority"] = map[string]any{"binding": admitted.authority.binding, "location": admitted.authority.location, "producer": admitted.authority.producer}
							data, err = json.Marshal(fields)
							if err != nil {
								t.Fatal(err)
							}
						}
						if err := json.Unmarshal(data, &a); err != nil {
							t.Fatal(err)
						}
					}
					if a.authority != nil {
						t.Fatal("public fields or serialization retained private authority")
					}
					if _, _, err := verifyAuthority(context.Background(), b, a); err == nil {
						t.Fatal("public claim passed private admission")
					}
					dir, before := authorityCacheFixture(t, populated)
					assertAuthorityRejectedWithoutMutation(t, b, a, dir, before, audit, decoded)
				})
			}
		}
	}
}

func TestNativeAuthorityAdmittedMutationsRejectBeforeCacheAndAudit(t *testing.T) {
	mutations := []string{"name", "inventory", "duplicate-case", "program-bytes", "program-path", "missing-program", "proof", "args", "env", "allowed-effect", "unknown-effect", "producer", "live-source", "missing-verifier", "missing-decoder", "backend-docker", "backend-image", "backend-worker", "backend-architecture", "backend-identity", "backend-live-unavailable"}
	for _, mutation := range mutations {
		for _, populated := range []bool{false, true} {
			for _, audit := range []bool{false, true} {
				name := mutation + map[bool]string{false: "/absent", true: "/populated"}[populated] + map[bool]string{false: "/normal", true: "/audit"}[audit]
				t.Run(name, func(t *testing.T) {
					b, a, liveErr, decoded := syntheticAuthorityFixture(t)
					switch mutation {
					case "name":
						a.Name = "other"
					case "inventory":
						a.Cases[1] = "TestGamma"
					case "duplicate-case":
						a.Cases[1] = "TestAlpha"
					case "program-bytes":
						if err := os.WriteFile(a.Program, []byte("def"), 0600); err != nil {
							t.Fatal(err)
						}
					case "program-path":
						a.Program = filepath.Join(t.TempDir(), "identical-program")
						if err := os.WriteFile(a.Program, []byte("abc"), 0600); err != nil {
							t.Fatal(err)
						}
					case "missing-program":
						if err := os.Remove(a.Program); err != nil {
							t.Fatal(err)
						}
					case "proof":
						a.Proof = strings.Repeat("2", 64)
					case "args":
						a.Args = []string{"-test.run=^TestAlpha$"}
					case "env":
						a.Env = []string{"GOMAXPROCS=2"}
					case "allowed-effect":
						a.Effects = []model.Effect{{Owner: "unit", Kind: "read", Path: "data/value"}}
					case "unknown-effect":
						a.Effects = []model.Effect{{Owner: "unit", Kind: "unknown"}}
					case "producer":
						a.authority.producer = strings.Repeat("3", 64)
					case "live-source":
						*liveErr = errors.New("independent producer source changed")
					case "missing-verifier":
						a.authority.verify = nil
					case "missing-decoder":
						a.authority.decode = nil
					case "backend-docker":
						b.Docker = "/other-docker"
					case "backend-image":
						b.Image = "sha256:" + strings.Repeat("4", 64)
					case "backend-worker":
						b.Worker = "/other-worker"
					case "backend-architecture":
						b.Architecture = "amd64"
					case "backend-identity":
						b.Identity = strings.Repeat("5", 64)
					case "backend-live-unavailable":
						// Private synthetic admission was healthy, but the unchanged
						// public descriptor cannot attest a live backend. Its probe
						// must precede creation of cache authentication material.
					}
					_, _, admissionErr := verifyAuthority(context.Background(), b, a)
					if mutation == "backend-live-unavailable" {
						if admissionErr != nil {
							t.Fatal("live backend probe control lost healthy private admission")
						}
					} else if admissionErr == nil {
						// The fixture backend is deliberately unavailable. Verify the
						// actual admission fault first so its later failure cannot mask
						// a mutation that the private seal wrongly accepted.
						t.Fatal("mutation passed private admission before unavailable backend")
					}
					dir, before := authorityCacheFixture(t, populated)
					assertAuthorityRejectedWithoutMutation(t, b, a, dir, before, audit, decoded)
				})
			}
		}
	}
}

func TestNativeAuthorityFIFOProgramRejectsInBoundedChild(t *testing.T) {
	const probe = "HAYAKU_NATIVE_AUTHORITY_FIFO_PROBE"
	if os.Getenv(probe) == "1" {
		b, a, _, decoded := syntheticAuthorityFixture(t)
		if data, err := readBounded(a.Program, 16); err != nil || string(data) != "abc" {
			t.Fatal("healthy regular program read failed")
		}
		fmt.Fprintln(os.Stdout, "regular-authority-control-ok")
		if err := os.Remove(a.Program); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(a.Program, 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(a.Program)
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("actual no-writer FIFO program missing")
		}
		// This is the public authority guard on an actual FIFO. The deterministic
		// regular-preflight-to-FIFO race belongs to the shared snapshot reader's
		// separate test; this probe supplies no influence qualification.
		if key, program, err := verifyAuthority(context.Background(), b, a); err == nil || key != "" || program != "" {
			t.Fatal("FIFO program received private admission")
		}
		fmt.Fprintln(os.Stdout, "fifo-authority-rejected")
		if _, err := readBounded(a.Program, 16); err == nil {
			t.Fatal("native program reader accepted FIFO")
		}
		fmt.Fprintln(os.Stdout, "fifo-reader-rejected")
		for _, populated := range []bool{false, true} {
			for _, audit := range []bool{false, true} {
				dir, before := authorityCacheFixture(t, populated)
				assertAuthorityRejectedWithoutMutation(t, b, a, dir, before, audit, decoded)
			}
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Parent-owned TMPDIR also removes child fixtures after a timeout regression.
	root := t.TempDir()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{
		Executable: os.Args[0], Args: []string{"-test.run=^TestNativeAuthorityFIFOProgramRejectsInBoundedChild$", "-test.v", "-test.count=1"},
	}, "", process.Environment(map[string]string{probe: "1", "TMPDIR": root}), 1024, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "regular-authority-control-ok\n") || !strings.Contains(string(out.Stdout), "fifo-authority-rejected\n") || !strings.Contains(string(out.Stdout), "fifo-reader-rejected\n") {
		t.Fatalf("bounded FIFO authority child failed controls: completed=%t exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
	t.Logf("actual FIFO authority and program-reader vetoes completed without a writer; child exited in %s", out.Duration)
}

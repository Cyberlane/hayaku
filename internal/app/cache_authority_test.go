package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/native"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

func cliAuthorityFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner := t.TempDir()
	if err := os.Chmod(owner, 0700); err != nil {
		t.Fatal(err)
	}
	policy, seed := filepath.Join(owner, "policy.json"), filepath.Join(owner, "issuer.seed")
	writeToolFixture(t, owner, "policy.json", passcache.Policy{Schema: 1, Namespace: "cli-jobs", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}})
	if err := os.WriteFile(seed, key.Seed(), 0600); err != nil {
		t.Fatal(err)
	}
	return policy, seed, key.Seed()
}

func cliCapsuleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	module := []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 4, 1, 96, 0, 0, 3, 2, 1, 0, 5, 3, 1, 0, 1, 7, 19, 2, 6, 109, 101, 109, 111, 114, 121, 2, 0, 6, 95, 115, 116, 97, 114, 116, 0, 0, 10, 4, 1, 2, 0, 11}
	if err := os.WriteFile(filepath.Join(root, "suite.wasm"), module, 0600); err != nil {
		t.Fatal(err)
	}
	c := capsuleConfig{Schema: 1, Name: "fixture", Module: "suite.wasm", Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("a", 64)}
	writeToolFixture(t, root, "capsule.json", c)
	writeToolFixture(t, root, "suite.json", suiteConfig{Schema: 1, Groups: []capsuleConfig{c}})
	return root
}

func TestPortableCacheJobHelper(t *testing.T) {
	if os.Getenv("HAYAKU_PORTABLE_CACHE_JOB") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Run(context.Background(), os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func portableCLIJob(t *testing.T, ctx context.Context, goCache string, args ...string) []byte {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	privateTmp := filepath.Join(goCache, "tmp")
	if err := os.MkdirAll(privateTmp, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := model.Command{Executable: binary, Args: append([]string{"-test.run=^TestPortableCacheJobHelper$", "--"}, args...)}
	out, err := process.RunEnvironmentLimits(ctx, cmd, t.TempDir(), []string{"HAYAKU_PORTABLE_CACHE_JOB=1", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOCACHE=" + goCache, "TMPDIR=" + privateTmp, "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off"}, 1<<20, 1<<20)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("disposable CLI job failed: completed=%v exit=%d error=%v diagnostic=%s", out.Completed, out.ExitCode, err, strings.TrimSpace(string(out.Stderr)))
	}
	return out.Stdout
}

func copyPortableReceipt(t *testing.T, source, destination, key string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, key+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "authority.key")); !os.IsNotExist(err) {
		t.Fatal("portable consumer transported/generated secret")
	}
}

func TestPortableCLIIndependentCapsuleJobs(t *testing.T) {
	root := cliCapsuleFixture(t)
	policy, key, seed := cliAuthorityFixture(t)
	producer, consumer := filepath.Join(t.TempDir(), "issuer"), filepath.Join(t.TempDir(), "verifier")
	goCache := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data := portableCLIJob(t, ctx, goCache, "capsule", "--root", root, "--config", "capsule.json", "--cache-dir", producer, "--cache-policy", policy, "--cache-signing-key", key)
	var first capsule.Result
	if json.Unmarshal(data, &first) != nil || !first.Passed || !first.Qualified || first.Mode != "executed" || first.CacheStatus != "stored" {
		t.Fatal("actual issuing job not qualified complete pass")
	}
	copyPortableReceipt(t, producer, consumer, first.Key)
	data = portableCLIJob(t, ctx, goCache, "capsule", "--root", root, "--config", "capsule.json", "--cache-dir", consumer, "--cache-policy", policy)
	var second capsule.Result
	if json.Unmarshal(data, &second) != nil || second.Key != first.Key || !second.Passed || !second.Qualified || second.Mode != "reused" {
		t.Fatal("independent verifier job did not reuse receipt")
	}
	data = portableCLIJob(t, ctx, goCache, "capsule", "--root", root, "--config", "capsule.json", "--cache-dir", consumer, "--cache-policy", policy, "--no-reuse")
	if json.Unmarshal(data, &second) != nil || !second.Passed || second.Mode != "executed" || second.CacheStatus != "verification-only" {
		t.Fatal("verifier audit job did not run without signing")
	}
	if _, err := os.Stat(filepath.Join(consumer, first.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("verifier audit republished pass")
	}
	data = portableCLIJob(t, ctx, goCache, "suite", "--root", root, "--config", "suite.json", "--cache-dir", consumer, "--cache-policy", policy)
	if !bytes.Contains(data, []byte(`"verification-only"`)) || bytes.Contains(data, seed) {
		t.Fatal("suite role forwarding or report secrecy failed")
	}
}

func TestPortableCLIExplicitBadPolicyCannotUseLocalAuthority(t *testing.T) {
	root := cliCapsuleFixture(t)
	cache := filepath.Join(t.TempDir(), "local")
	if status, _, _ := tool(t, root, "capsule", "--config", "capsule.json", "--cache-dir", cache); status != 0 {
		t.Fatal("local healthy control failed")
	}
	before := map[string][]byte{}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(cache, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		before[entry.Name()] = data
	}
	policy, key, _ := cliAuthorityFixture(t)
	for _, args := range [][]string{{"--cache-policy", ""}, {"--cache-policy", filepath.Join(t.TempDir(), "missing")}, {"--cache-signing-key", key}, {"--cache-policy", policy, "--cache-signing-key", ""}, {"--cache-policy", policy, "--cache-signing-key", filepath.Join(t.TempDir(), "missing")}, {"--cache-policy", filepath.Join(root, "capsule.json")}} {
		full := append([]string{"capsule", "--config", "capsule.json", "--cache-dir", cache}, args...)
		if status, data, _ := tool(t, root, full...); status == 0 || len(data) != 0 {
			t.Fatal("bad explicit portable policy fell back to local pass")
		}
		current, err := os.ReadDir(cache)
		if err != nil || len(current) != len(before) {
			t.Fatal("invalid policy changed local cache membership")
		}
		for name, original := range before {
			data, err := os.ReadFile(filepath.Join(cache, name))
			if err != nil || !bytes.Equal(data, original) {
				t.Fatal("invalid policy changed local cache bytes")
			}
		}
	}
	if status, _, _ := tool(t, root, "capsule", "--config", "capsule.json", "--cache-policy", policy); status == 0 {
		t.Fatal("portable policy without cache silently disabled")
	}
	if status, _, _ := tool(t, root, "inputs", "--config", "capsule.json", "--cache-policy", policy); status == 0 {
		t.Fatal("unrelated tool admitted cache policy")
	}
}

func TestPortableCLIEmptySigningKeyPreservesSignedCache(t *testing.T) {
	root := cliCapsuleFixture(t)
	policy, key, _ := cliAuthorityFixture(t)
	cache := filepath.Join(t.TempDir(), "signed")
	args := []string{"capsule", "--config", "capsule.json", "--cache-dir", cache, "--cache-policy", policy}
	if status, _, _ := tool(t, root, append(append([]string(nil), args...), "--cache-signing-key", key)...); status != 0 {
		t.Fatal("signed healthy control failed")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatal("signed healthy control did not store exactly one receipt", err)
	}
	path := filepath.Join(cache, entries[0].Name())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	status, data, _ := tool(t, root, args...)
	var healthy capsule.Result
	if status != 0 || json.Unmarshal(data, &healthy) != nil || healthy.Mode != "reused" || !healthy.Passed {
		t.Fatal("public verifier healthy control did not reuse the signed receipt")
	}
	if status, data, _ := tool(t, root, append(append([]string(nil), args...), "--cache-signing-key", "")...); status == 0 || len(data) != 0 {
		t.Fatal("explicit empty signing key accepted or emitted a result")
	}
	current, err := os.ReadDir(cache)
	if err != nil || len(current) != 1 || current[0].Name() != entries[0].Name() {
		t.Fatal("explicit empty signing key changed signed cache membership", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("explicit empty signing key changed signed receipt bytes", err)
	}
}

func TestInstalledDarwinPortableCLIIndependentNativeJobs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires actual installed macOS native enforcement")
	}
	root := t.TempDir()
	nativeAppWrite(t, root, "go.mod", "module sample\ngo 1.26\n")
	nativeAppWrite(t, root, "sample_test.go", "package sample;import \"testing\";func TestValue(t *testing.T){if 1+1!=2{t.Fatal(\"wrong\")}}")
	writeToolFixture(t, root, "native.json", native.Config{Schema: 1, Platform: "darwin", Architecture: runtime.GOARCH, Packages: []string{"."}})
	policy, key, _ := cliAuthorityFixture(t)
	producer, consumer := filepath.Join(t.TempDir(), "issuer"), filepath.Join(t.TempDir(), "verifier")
	goCache := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	data := portableCLIJob(t, ctx, goCache, "native", "--root", root, "--cache-dir", producer, "--cache-policy", policy, "--cache-signing-key", key)
	var first native.Report
	if json.Unmarshal(data, &first) != nil || !first.Passed || !first.Complete || len(first.Groups) != 1 || !first.Groups[0].Executed || !first.Groups[0].Qualified || first.Groups[0].Context != "darwin-"+runtime.GOARCH {
		t.Fatal("actual native issuer did not execute qualified group")
	}
	copyPortableReceipt(t, producer, consumer, first.Groups[0].Key)
	data = portableCLIJob(t, ctx, goCache, "native", "--root", root, "--cache-dir", consumer, "--cache-policy", policy)
	var second native.Report
	if json.Unmarshal(data, &second) != nil || !second.Passed || !second.Complete || len(second.Groups) != 1 || !second.Groups[0].Reused || second.Groups[0].Executed || second.Groups[0].Key != first.Groups[0].Key {
		t.Fatal("actual native verifier did not reuse unchanged signed group")
	}
}

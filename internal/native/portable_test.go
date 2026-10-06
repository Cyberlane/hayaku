package native

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/passcache"
)

func nativePortableAuthority(t *testing.T) (*passcache.Authority, *passcache.Authority) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := passcache.Policy{Schema: 1, Namespace: "native-fixture", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}}
	i, err := passcache.NewAuthority(p, key.Seed())
	if err != nil {
		t.Fatal(err)
	}
	v, err := passcache.NewAuthority(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return i, v
}

type nativePersistedPolicyFault struct {
	context.Context
	cache, policy string
	once          sync.Once
	done          chan error
}

func (c *nativePersistedPolicyFault) Err() error {
	entries, _ := os.ReadDir(c.cache)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			c.once.Do(func() { c.done <- os.Chmod(c.policy, 0400) })
			break
		}
	}
	return c.Context.Err()
}

func TestInstalledDarwinPortableAuthorityPostPersistence(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires actual installed macOS native enforcement")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(t.TempDir(), "authority")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	policy, seed := filepath.Join(owner, "policy.json"), filepath.Join(owner, "issuer.seed")
	data, err := json.Marshal(passcache.Policy{Schema: 1, Namespace: "native-store-fault", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed, key.Seed(), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := passcache.LoadAuthority(context.Background(), policy, seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	write(t, root, "sample_test.go", "package sample;import \"testing\";func TestValue(t *testing.T){if 1+1!=2{t.Fatal(\"wrong\")}}")
	cache := filepath.Join(t.TempDir(), "passes")
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx := &nativePersistedPolicyFault{Context: parent, cache: cache, policy: policy, done: make(chan error, 1)}
	r, err := GoWithOptions(ctx, root, Config{Schema: 1, Platform: "darwin", Architecture: runtime.GOARCH, Packages: []string{"."}}, ExecutionOptions{CacheDir: cache, Authority: a})
	select {
	case faultErr := <-ctx.done:
		if faultErr != nil {
			t.Fatal(faultErr)
		}
	default:
		t.Fatal("actual native persisted receipt boundary not reached")
	}
	if err == nil || r.Complete || r.Passed {
		t.Fatal("native authority drift after signing remained aggregate success")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatal("native drift cleanup left signed authority")
	}
}

func TestPortableNativeCancelledAuditRevokesBeforeCapture(t *testing.T) {
	issuer, verifier := nativePortableAuthority(t)
	dir := filepath.Join(t.TempDir(), "passes")
	ownKey, otherKey := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, entry := range []struct{ domain, key string }{{"hayaku-native-pure-pass-v1", ownKey}, {"other", otherKey}} {
		c, err := passcache.OpenWithAuthority(dir, entry.domain, issuer)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Store(context.Background(), entry.key, true); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := ExecutionOptions{CacheDir: dir, Audit: true, Authority: verifier}
	r, err := GoWithOptions(ctx, t.TempDir(), Config{Schema: 1, Architecture: runtime.GOARCH, Packages: []string{"."}}, options)
	if !errors.Is(err, context.Canceled) || r.Complete || r.Passed || len(r.Groups) != 0 {
		t.Fatal("cancelled audit captured/executed or lost cancellation", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ownKey+".json")); !os.IsNotExist(err) {
		t.Fatal("pre-cancelled audit left own signed receipt")
	}
	c, err := passcache.OpenWithAuthority(dir, "other", verifier)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, status := c.Load(otherKey); status != "hit" {
		t.Fatal("audit removed unrelated domain", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "authority.key")); !os.IsNotExist(err) {
		t.Fatal("portable audit generated local secret")
	}
}

func TestPortableNativePublicArtifactsCannotMutateCache(t *testing.T) {
	issuer, verifier := nativePortableAuthority(t)
	dir := filepath.Join(t.TempDir(), "passes")
	key := strings.Repeat("c", 64)
	c, err := passcache.OpenWithAuthority(dir, "hayaku-native-pure-pass-v1", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range []bool{false, true} {
		for _, envelope := range []*Envelope{nil, {}} {
			a := Artifact{Name: "unit", Proof: strings.Repeat("d", 64), Envelope: envelope}
			r, err := ExecuteWithOptions(context.Background(), Backend{}, a, ExecutionOptions{CacheDir: dir, Audit: audit, Authority: verifier}, nil)
			if err == nil || r.Complete || r.Passed || r.Qualified || r.Reused {
				t.Fatal("public artifact acquired portable authority")
			}
			after, err := os.ReadFile(filepath.Join(dir, key+".json"))
			if err != nil || string(after) != string(before) {
				t.Fatal("unqualified artifact mutated portable cache")
			}
		}
	}
}

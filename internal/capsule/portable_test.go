package capsule

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Cyberlane/hayaku/internal/passcache"
)

func TestPortableCapsuleActualExecutionTransportAndVerifierAudit(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := passcache.Policy{Schema: 1, Namespace: "capsule-fixture", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}}
	issuer, err := passcache.NewAuthority(policy, key.Seed())
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := passcache.NewAuthority(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
	producer := Options{CacheDir: filepath.Join(t.TempDir(), "producer"), Authority: issuer}
	pass, err := Run(context.Background(), request, producer)
	if err != nil || !pass.Complete || !pass.Qualified || !pass.Passed || pass.Mode != "executed" || pass.CacheStatus != "stored" {
		t.Fatal("actual issuer execution did not mint complete pass", err)
	}
	consumer := Options{CacheDir: filepath.Join(t.TempDir(), "consumer"), Authority: verifier}
	if err := os.MkdirAll(consumer.CacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(producer.CacheDir, pass.Key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer.CacheDir, pass.Key+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	hit, err := Run(context.Background(), request, consumer)
	if err != nil || hit.Key != pass.Key || hit.Mode != "reused" || !hit.Passed || !hit.Complete || hit.StdoutDigest != pass.StdoutDigest {
		t.Fatal("receipt-only transport did not reuse exact contract", err)
	}
	consumer.NoReuse = true
	audit, err := Run(context.Background(), request, consumer)
	if err != nil || audit.Mode != "executed" || !audit.Passed || audit.CacheStatus != "verification-only" {
		t.Fatal("verifier audit did not execute without signing", err)
	}
	if _, err := os.Stat(filepath.Join(consumer.CacheDir, pass.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("verifier audit republished signed pass")
	}
	consumer.NoReuse = false
	miss, err := Run(context.Background(), request, consumer)
	if err != nil || miss.Mode != "executed" || !miss.Passed || miss.CacheStatus != "verification-only" {
		t.Fatal("verifier miss did not run fresh", err)
	}
	failedRequest := request
	failedRequest.Module = wasmCommand("", "", nil, nil, false, []byte{0}, nil)
	failed, err := Run(context.Background(), failedRequest, consumer)
	if err == nil || failed.Passed || failed.Mode != "executed" {
		t.Fatal("actual trap became authenticated pass")
	}
	files, err := os.ReadDir(consumer.CacheDir)
	if err != nil || len(files) != 0 {
		t.Fatal("verifier failure/miss created cache authority")
	}
	// Receipt authentication does not change existing artifact/backend keys.
	local, err := Run(context.Background(), request, Options{})
	if err != nil || local.Key != pass.Key || local.BackendDigest != pass.BackendDigest {
		t.Fatal("portable authentication changed execution identity")
	}
	if bytes.Contains(data, key.Seed()) {
		t.Fatal("signing seed leaked into receipt")
	}
}

// The fault waits for an actual persisted receipt rather than a call count or
// production hook. The next context poll models a host policy update at that
// filesystem boundary; the context itself stays live.
type persistedPolicyFault struct {
	context.Context
	cache, policy string
	once          sync.Once
	done          chan error
}

func (c *persistedPolicyFault) Err() error {
	entries, _ := os.ReadDir(c.cache)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			c.once.Do(func() { c.done <- os.Chmod(c.policy, 0400) })
			break
		}
	}
	return c.Context.Err()
}

func TestPortableCapsuleAuthorityDriftAfterPersistence(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(t.TempDir(), "authority")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	policy, seed := filepath.Join(owner, "policy.json"), filepath.Join(owner, "issuer.seed")
	data, err := json.Marshal(passcache.Policy{Schema: 1, Namespace: "actual-store-fault", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}})
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
	cache := filepath.Join(t.TempDir(), "passes")
	ctx := &persistedPolicyFault{Context: context.Background(), cache: cache, policy: policy, done: make(chan error, 1)}
	r, err := Run(ctx, validRequest(wasmCommand("", "", nil, nil, false, nil, nil)), Options{CacheDir: cache, Authority: a})
	select {
	case faultErr := <-ctx.done:
		if faultErr != nil {
			t.Fatal(faultErr)
		}
	default:
		t.Fatal("actual persisted receipt boundary not reached")
	}
	if err == nil || r.Complete || r.Qualified || r.Passed || r.Reason != "cache-authority-changed" {
		t.Fatal("post-store authority drift remained successful")
	}
	if _, err := os.Stat(filepath.Join(cache, r.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("drifted signed copy remained")
	}
}

func TestPortableCapsuleUnavailableStorageExecutesFresh(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a, err := passcache.NewAuthority(passcache.Policy{Schema: 1, Namespace: "unavailable", Epoch: 1, PublicKeys: []string{hex.EncodeToString(pub)}}, key.Seed())
	if err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(obstruction, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), validRequest(wasmCommand("", "", nil, nil, false, nil, nil)), Options{CacheDir: filepath.Join(obstruction, "passes"), Authority: a})
	if err != nil || !r.Complete || !r.Passed || !r.Qualified || r.Mode != "executed" || r.CacheStatus != "unavailable" {
		t.Fatal("unavailable storage did not execute complete fresh contract", err)
	}
	data, err := os.ReadFile(obstruction)
	if err != nil || string(data) != "x" {
		t.Fatal("unavailable storage was altered")
	}
}

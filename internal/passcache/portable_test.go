package passcache

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func portableAuthorities(t *testing.T) (*Authority, *Authority, Policy, []byte) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{Schema: 1, Namespace: "fixture/portable", Epoch: 7, PublicKeys: []string{hex.EncodeToString(pub)}}
	issuer, err := NewAuthority(p, private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewAuthority(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return issuer, verifier, p, private.Seed()
}

func TestPortableReceiptSeparateVerifierAndAudit(t *testing.T) {
	issuer, verifier, _, _ := portableAuthorities(t)
	key, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	producer, err := OpenWithAuthority(filepath.Join(t.TempDir(), "issuer-job"), "native", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if err := producer.Store(context.Background(), key, map[string]bool{"passed": true}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "verifier-job")
	consumer, err := OpenWithAuthority(dir, "native", verifier)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	data, err := readPrivate(producer.root, key+".json", MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if value, status := consumer.Load(key); status != "hit" || string(value) != `{"passed":true}` {
		t.Fatal("transported signed pass not verified", status)
	}
	if consumer.CanStore() || !producer.CanStore() {
		t.Fatal("issuance role not separated")
	}
	if err := consumer.Store(context.Background(), other, true); err == nil {
		t.Fatal("verification-only consumer minted receipt")
	}
	if _, status := consumer.Load(other); status != "miss" {
		t.Fatal(status)
	}
	for _, root := range []*Cache{producer, consumer} {
		if _, err := root.root.Lstat("authority.key"); !os.IsNotExist(err) {
			t.Fatal("portable mode created a local signing secret")
		}
	}
	foreign, err := OpenWithAuthority(dir, "wasi", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := foreign.Store(context.Background(), other, true); err != nil {
		t.Fatal(err)
	}
	if err := consumer.RevokeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, status := consumer.Load(key); status != "miss" {
		t.Fatal("verifier audit left own pass", status)
	}
	if _, status := foreign.Load(other); status != "hit" {
		t.Fatal("verifier audit removed another domain", status)
	}
}

func TestPortableEnvelopeSubstitution(t *testing.T) {
	issuer, verifier, p, _ := portableAuthorities(t)
	key := strings.Repeat("c", 64)
	dir := filepath.Join(t.TempDir(), "passes")
	producer, err := OpenWithAuthority(dir, "native", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	consumer, err := OpenWithAuthority(dir, "native", verifier)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	if err := producer.Store(context.Background(), key, map[string]bool{"passed": true}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	faults := map[string]func(*envelope){
		"namespace": func(e *envelope) { e.Namespace = "another" },
		"epoch":     func(e *envelope) { e.Epoch++ },
		"domain":    func(e *envelope) { e.Domain = "wasi" },
		"artifact":  func(e *envelope) { e.Key = strings.Repeat("d", 64) },
		"value":     func(e *envelope) { e.Value = json.RawMessage(`{"passed":false}`) },
		"signature": func(e *envelope) { e.Signature = strings.Repeat("0", 128) },
		"issuer":    func(e *envelope) { e.Issuer = strings.Repeat("0", 64) },
		"schema":    func(e *envelope) { e.Schema = 2 },
		"mac":       func(e *envelope) { e.MAC = strings.Repeat("0", 64) },
	}
	for name, mutate := range faults {
		t.Run(name, func(t *testing.T) {
			var e envelope
			if err := json.Unmarshal(original, &e); err != nil {
				t.Fatal(err)
			}
			mutate(&e)
			data, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, key+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, status := consumer.Load(key); status != "invalid" {
				t.Fatal("substitution authenticated", status)
			}
		})
	}
	for _, data := range [][]byte{append([]byte(" "), original...), bytes.Replace(original, []byte(`"schema":3`), []byte(`"schema":3,"schema":3`), 1), append(original, []byte("{}")...), bytes.Repeat([]byte("x"), MaxBytes+1)} {
		if err := os.WriteFile(filepath.Join(dir, key+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, status := consumer.Load(key); status != "invalid" {
			t.Fatal("noncanonical receipt authenticated", status)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Policy){func(p *Policy) { p.Namespace = "another" }, func(p *Policy) { p.Epoch++ }, func(p *Policy) {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		p.PublicKeys = []string{hex.EncodeToString(pub)}
	}} {
		changed := p
		changed.PublicKeys = append([]string(nil), p.PublicKeys...)
		change(&changed)
		foreign, err := NewAuthority(changed, nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := OpenWithAuthority(dir, "native", foreign)
		if err != nil {
			t.Fatal(err)
		}
		if _, status := c.Load(key); status != "invalid" {
			t.Fatal("foreign policy authenticated", status)
		}
		c.Close()
	}
	local, err := Open(dir, "native")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if _, status := local.Load(key); status != "invalid" {
		t.Fatal("local mode accepted signed schema", status)
	}
	if err := local.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	localBytes, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, status := consumer.Load(key); status != "invalid" {
		t.Fatal("portable mode accepted legacy HMAC", status)
	}
	if err := consumer.Store(context.Background(), key, true); err == nil {
		t.Fatal("verifier rewrote local receipt")
	}
	after, _ := os.ReadFile(filepath.Join(dir, key+".json"))
	if !bytes.Equal(after, localBytes) {
		t.Fatal("verifier mutated receipt")
	}
}

func TestPortableAuthorityInputBoundsAndCopies(t *testing.T) {
	_, _, p, seed := portableAuthorities(t)
	for _, key := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("a", 1<<20), strings.Repeat("G", 64)} {
		bad := p
		bad.PublicKeys = []string{key}
		if _, err := NewAuthority(bad, nil); err == nil {
			t.Fatal("invalid key admitted")
		}
	}
	for _, namespace := range []string{"", "*", "spaces here", "line\n", strings.Repeat("a", 257)} {
		bad := p
		bad.Namespace = namespace
		if _, err := NewAuthority(bad, nil); err == nil {
			t.Fatal("invalid namespace admitted")
		}
	}
	bad := p
	bad.PublicKeys = append(bad.PublicKeys, p.PublicKeys[0])
	if _, err := NewAuthority(bad, nil); err == nil {
		t.Fatal("duplicate issuer admitted")
	}
	bad = p
	bad.Epoch = 0
	if _, err := NewAuthority(bad, nil); err == nil {
		t.Fatal("zero epoch admitted")
	}
	if _, err := NewAuthority(p, make([]byte, 32)); err == nil {
		t.Fatal("foreign issuer key admitted")
	}
	a, err := NewAuthority(p, seed)
	if err != nil {
		t.Fatal(err)
	}
	p.PublicKeys[0] = strings.Repeat("0", 64)
	clear(seed)
	c, err := OpenWithAuthority(filepath.Join(t.TempDir(), "cache"), "native", a)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Store(context.Background(), strings.Repeat("e", 64), true); err != nil {
		t.Fatal("caller mutation corrupted authority", err)
	}
}

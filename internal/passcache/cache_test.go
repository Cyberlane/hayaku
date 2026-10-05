package passcache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticatedPrivateDomainAndTamper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	key := strings.Repeat("a", 64)
	c, err := Open(dir, "native")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Store(context.Background(), key, map[string]bool{"passed": true}); err != nil {
		t.Fatal(err)
	}
	if _, status := c.Load(key); status != "hit" {
		t.Fatal(status)
	}
	foreign, err := Open(dir, "wasi")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, status := foreign.Load(key); status != "invalid" {
		t.Fatal("cross-contract receipt accepted", status)
	}
	data, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), `"passed":true`, `"passed":false`, 1)
	if err = os.WriteFile(filepath.Join(dir, key+".json"), []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, status := c.Load(key); status != "invalid" {
		t.Fatal("tampered receipt accepted", status)
	}
	if err = c.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(dir, key+".json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, status := c.Load(key); status != "invalid" {
		t.Fatal("public receipt accepted", status)
	}
	if _, status := c.Load("../../outside"); status != "invalid" {
		t.Fatal("path key accepted")
	}
}
func TestCancelledStoreAndSymlinksNeverAuthorize(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache"), "native")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := strings.Repeat("b", 64)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = c.Store(ctx, key, true); err == nil {
		t.Fatal("cancelled store accepted")
	}
	if _, status := c.Load(key); status != "miss" {
		t.Fatal(status)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(outside, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "authority.key")); err != nil {
		t.Fatal(err)
	}
	if cache, err := Open(dir, "native"); err == nil {
		cache.Close()
		t.Fatal("linked authority accepted")
	}
}
func TestDomainRevocation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	native, err := Open(dir, "native")
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	wasi, err := Open(dir, "wasi")
	if err != nil {
		t.Fatal(err)
	}
	defer wasi.Close()
	nativeKey, wasiKey := strings.Repeat("c", 64), strings.Repeat("d", 64)
	if err = native.Store(context.Background(), nativeKey, true); err != nil {
		t.Fatal(err)
	}
	if err = wasi.Store(context.Background(), wasiKey, true); err != nil {
		t.Fatal(err)
	}
	if err = native.RevokeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, status := native.Load(nativeKey); status != "miss" {
		t.Fatal(status)
	}
	if _, status := wasi.Load(wasiKey); status != "hit" {
		t.Fatal("other domain revoked", status)
	}
}

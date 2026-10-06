package passcache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevokeDomainUsesCapturedVerifierAfterPolicyDrift(t *testing.T) {
	issuer, _, policy, seed := portableAuthorities(t)
	policyPath, _ := writeProtectedAuthority(t, filepath.Join(t.TempDir(), "protected"), policy, seed)
	directory := filepath.Join(t.TempDir(), "receipts")
	verifier, err := LoadAuthority(context.Background(), policyPath, "", []string{directory})
	if err != nil {
		t.Fatal(err)
	}
	own, err := OpenWithAuthority(directory, "own", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	foreign, err := OpenWithAuthority(directory, "foreign", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	ownKey, foreignKey := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for key, cache := range map[string]*Cache{ownKey: own, foreignKey: foreign} {
		if err := cache.Store(context.Background(), key, true); err != nil {
			t.Fatal(err)
		}
		if _, status := cache.Load(key); status != "hit" {
			t.Fatal("healthy signed fixture did not authenticate", status)
		}
	}
	foreignPath := filepath.Join(directory, foreignKey+".json")
	before, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background()); err == nil {
		t.Fatal("actual trusted policy drift was not detected")
	}
	audit, err := BeginAudit(directory, "own", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Finish(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, ownKey+".json")); !os.IsNotExist(err) {
		t.Fatal("failed audit retained the authenticated local copy", err)
	}
	after, err := os.ReadFile(foreignPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("cleanup changed a foreign provider receipt", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "authority.key")); !os.IsNotExist(err) {
		t.Fatal("verifier cleanup created local signing authority", err)
	}
}

func TestAuditPinsOriginalRootAndPreservesReplacement(t *testing.T) {
	for _, portable := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "portable"}[portable], func(t *testing.T) {
			var issuer, verifier *Authority
			if portable {
				issuer, verifier, _, _ = portableAuthorities(t)
			}
			owner := t.TempDir()
			directory := filepath.Join(owner, "selected")
			cache, err := OpenWithAuthority(directory, "own", issuer)
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			key := strings.Repeat("c", 64)
			if err := cache.Store(context.Background(), key, true); err != nil {
				t.Fatal(err)
			}
			if _, status := cache.Load(key); status != "hit" {
				t.Fatal(status)
			}
			audit, err := BeginAudit(directory, "own", verifier)
			if err != nil {
				t.Fatal(err)
			}
			retained := filepath.Join(owner, "original")
			if err := os.Rename(directory, retained); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(directory, "replacement")
			if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := audit.Finish(false); err == nil {
				t.Fatal("retargeted audit reported success")
			}
			if _, err := os.Lstat(filepath.Join(retained, key+".json")); !os.IsNotExist(err) {
				t.Fatal("original receipt survived", err)
			}
			value, err := os.ReadFile(marker)
			if err != nil || string(value) != "preserve" {
				t.Fatal("replacement directory changed", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 {
				t.Fatal("cleanup created replacement authority", err)
			}
		})
	}
}

func TestAuditAbsentAndMalformedAuthorityNeverCreateSecrets(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	audit, err := BeginAudit(missing, "own", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Finish(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("failed prevalidation audit created a cache", err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "authority.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	healthy, err := BeginAudit(directory, "own", nil)
	if err != nil {
		t.Fatal("healthy existing authority rejected", err)
	}
	if err := healthy.Finish(false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginAudit(directory, "own", nil); err == nil || !strings.Contains(err.Error(), "existing audit authority unavailable") {
		t.Fatal("malformed existing authority admitted")
	}
	value, err := os.ReadFile(key)
	if err != nil || string(value) != "malformed" {
		t.Fatal("cleanup replaced existing authority", err)
	}
}

func TestAuditObservesCreatedRootBeforeLaterRename(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "empty"}[empty], func(t *testing.T) {
			owner := t.TempDir()
			directory := filepath.Join(owner, "cache")
			if empty {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			audit, err := BeginAudit(directory, "own", nil)
			if err != nil {
				t.Fatal(err)
			}
			cache, err := Open(directory, "own")
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			if err := audit.Observe(cache); err != nil {
				t.Fatal(err)
			}
			key := strings.Repeat("d", 64)
			if err := cache.Store(context.Background(), key, true); err != nil {
				t.Fatal(err)
			}
			if _, status := cache.Load(key); status != "hit" {
				t.Fatal("newly executed pass not authenticated", status)
			}
			retained := filepath.Join(owner, "retained")
			if err := os.Rename(directory, retained); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := audit.Finish(true); err == nil {
				t.Fatal("later root drift was not reported")
			}
			if _, err := os.Lstat(filepath.Join(retained, key+".json")); !os.IsNotExist(err) {
				t.Fatal("earlier new pass survived later failure", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("replacement modified", err)
			}
		})
	}
}

func TestAuditRetainsOriginalKeyOnActualKeyDrift(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "inode"}[replace], func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "cache")
			cache, err := Open(directory, "own")
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			key := strings.Repeat("e", 64)
			if err := cache.Store(context.Background(), key, true); err != nil {
				t.Fatal(err)
			}
			audit, err := BeginAudit(directory, "own", nil)
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(directory, "authority.key")
			original, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if replace {
				replacement := filepath.Join(directory, "new-key")
				if err := os.WriteFile(replacement, original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, filename); err != nil {
					t.Fatal(err)
				}
			} else {
				original[0] ^= 0xff
				if err := os.WriteFile(filename, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := audit.Finish(false); err == nil {
				t.Fatal("actual key drift accepted")
			}
			if _, err := os.Lstat(filepath.Join(directory, key+".json")); !os.IsNotExist(err) {
				t.Fatal("original-key pass survived", err)
			}
		})
	}
}

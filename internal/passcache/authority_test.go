package passcache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func writeProtectedAuthority(t *testing.T, dir string, p Policy, seed []byte) (string, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	policy, key := filepath.Join(dir, "policy.json"), filepath.Join(dir, "issuer.seed")
	if err := os.WriteFile(policy, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, seed, 0600); err != nil {
		t.Fatal(err)
	}
	return policy, key
}

func TestProtectedAuthorityNoWriterFIFOAndUnavailablePortableCache(t *testing.T) {
	if os.Getenv("HAYAKU_AUTHORITY_FIFO_PROBE") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: binary, Args: []string{"-test.run=^TestProtectedAuthorityNoWriterFIFOAndUnavailablePortableCache$"}}, t.TempDir(), []string{"HAYAKU_AUTHORITY_FIFO_PROBE=1", "TMPDIR=" + t.TempDir()}, 1<<20, 1<<20)
		if err != nil || !out.Completed || out.ExitCode != 0 {
			t.Fatal("bounded FIFO authority subprocess failed", err)
		}
		return
	}

	_, _, p, seed := portableAuthorities(t)
	owner := t.TempDir()
	policy, key := writeProtectedAuthority(t, filepath.Join(owner, "authority"), p, seed)
	a, err := LoadAuthority(context.Background(), policy, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(key, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority(context.Background(), policy, key, nil); err == nil {
		t.Fatal("FIFO signing key admitted")
	}

	if err := a.Verify(context.Background()); err == nil {
		t.Fatal("captured authority key replaced by FIFO accepted")
	}
	obstruction := filepath.Join(owner, "not-a-directory")
	if err := os.WriteFile(obstruction, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := OpenWithAuthority(filepath.Join(obstruction, "cache"), "native", a); err == nil || c != nil {
		t.Fatal("unavailable portable cache acquired authority")
	}
}

func TestProtectedAuthorityHealthyDriftAndRevocation(t *testing.T) {
	_, _, p, seed := portableAuthorities(t)
	for _, fault := range []string{"bytes", "inode", "mode", "parent", "alias"} {
		t.Run(fault, func(t *testing.T) {
			owner := t.TempDir()
			policy, key := writeProtectedAuthority(t, filepath.Join(owner, "authority"), p, seed)
			if fault == "alias" {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(owner, alias); err != nil {
					t.Fatal(err)
				}
				policy, key = filepath.Join(alias, "authority", "policy.json"), filepath.Join(alias, "authority", "issuer.seed")
			}
			dir := filepath.Join(t.TempDir(), "passes")
			a, err := LoadAuthority(context.Background(), policy, key, []string{filepath.Join(owner, "missing-source"), dir})
			if err != nil {
				t.Fatal("healthy policy not admitted", err)
			}
			c, err := OpenWithAuthority(dir, "native", a)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			rkey := strings.Repeat("f", 64)
			if err := c.Store(context.Background(), rkey, true); err != nil {
				t.Fatal(err)
			}
			if _, status := c.Load(rkey); status != "hit" {
				t.Fatal(status)
			}
			switch fault {
			case "bytes":
				if err := os.WriteFile(key, make([]byte, 32), 0600); err != nil {
					t.Fatal(err)
				}
			case "inode":
				data, err := os.ReadFile(policy)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(policy)
				if err != nil {
					t.Fatal(err)
				}
				replacement := policy + ".new"
				if err := os.WriteFile(replacement, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, policy); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(policy, 0400); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.WriteFile(filepath.Join(filepath.Dir(policy), "unrelated"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			case "alias":
				target := filepath.Join(owner, "replacement")
				writeProtectedAuthority(t, filepath.Join(target, "authority"), p, seed)
				if err := os.Remove(filepath.Dir(filepath.Dir(policy))); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Dir(filepath.Dir(policy))); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Verify(context.Background()); err == nil {
				t.Fatal("authority drift not detected")
			}
			if _, status := c.Load(rkey); status != "invalid" {
				t.Fatal("drifted policy reused", status)
			}
			if err := c.Store(context.Background(), rkey, true); err == nil {
				t.Fatal("drifted authority signed")
			}
			if err := c.RevokeAll(context.Background()); err != nil {
				t.Fatal("captured-policy cleanup failed", err)
			}
			if _, err := os.Stat(filepath.Join(dir, rkey+".json")); !os.IsNotExist(err) {
				t.Fatal("old signed copy survived policy-drift cleanup")
			}
		})
	}
}

func TestProtectedAuthorityRejectsInvalidPolicyAndKey(t *testing.T) {
	_, _, p, seed := portableAuthorities(t)
	for _, fault := range []string{"duplicate-field", "unknown-field", "trailing", "oversize", "public-policy", "public-key", "linked-policy", "linked-key", "bad-seed", "duplicate-issuer", "source-owned", "cache-owned"} {
		t.Run(fault, func(t *testing.T) {
			owner := t.TempDir()
			policy, key := writeProtectedAuthority(t, filepath.Join(owner, "private"), p, seed)
			forbidden := []string{filepath.Join(owner, "source"), filepath.Join(owner, "cache")}
			switch fault {
			case "duplicate-field", "unknown-field", "trailing", "oversize":
				data, err := os.ReadFile(policy)
				if err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "duplicate-field":
					data = append([]byte(`{"schema":1,`), data[1:]...)
				case "unknown-field":
					data = append([]byte(`{"qualified":true,`), data[1:]...)
				case "trailing":
					data = append(data, []byte("{}")...)
				case "oversize":
					data = []byte(strings.Repeat("x", (64<<10)+1))
				}
				if err := os.WriteFile(policy, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "public-policy":
				if err := os.Chmod(policy, 0644); err != nil {
					t.Fatal(err)
				}
			case "public-key":
				if err := os.Chmod(key, 0644); err != nil {
					t.Fatal(err)
				}
			case "linked-policy", "linked-key":
				name := policy
				if fault == "linked-key" {
					name = key
				}
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".original", name); err != nil {
					t.Fatal(err)
				}
			case "bad-seed":
				if err := os.WriteFile(key, make([]byte, 32), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate-issuer":
				bad := p
				bad.PublicKeys = append(append([]string(nil), p.PublicKeys...), p.PublicKeys[0])
				writeProtectedAuthority(t, filepath.Dir(policy), bad, seed)
			case "source-owned":
				forbidden[0] = owner
			case "cache-owned":
				forbidden[1] = owner
			}
			if _, err := LoadAuthority(context.Background(), policy, key, forbidden); err == nil {
				t.Fatal("invalid policy/key boundary admitted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy, key := writeProtectedAuthority(t, t.TempDir(), p, seed)
	if _, err := LoadAuthority(ctx, policy, key, nil); !errors.Is(err, context.Canceled) {
		// Filesystem errors are sanitized, but an already cancelled caller must
		// remain identifiable without touching policy or creating cache keys.
		t.Fatal("typed cancellation lost", err)
	}
}

func TestProtectedAuthorityDirectoryIdentityAliases(t *testing.T) {
	_, _, p, seed := portableAuthorities(t)
	for _, names := range [][2]string{{"Checkout", "CHECKOUT"}, {"caf\u00e9", "cafe\u0301"}} {
		t.Run(names[0], func(t *testing.T) {
			owner := t.TempDir()
			realRoot := filepath.Join(owner, names[0])
			aliasRoot := filepath.Join(owner, names[1])
			policy, key := writeProtectedAuthority(t, filepath.Join(realRoot, "private"), p, seed)
			inside, err := os.Stat(realRoot)
			if err != nil {
				t.Fatal(err)
			}
			alias, err := os.Stat(aliasRoot)
			if err == nil && os.SameFile(inside, alias) {
				aliasPolicy := filepath.Join(aliasRoot, "private", "policy.json")
				aliasKey := filepath.Join(aliasRoot, "private", "issuer.seed")
				for _, forbidden := range [][]string{{realRoot}, {filepath.Join(owner, "absent-source"), realRoot}} {
					if _, err := LoadAuthority(context.Background(), aliasPolicy, aliasKey, forbidden); err == nil {
						t.Fatal("actual source/cache alias admitted")
					}
				}
			} else {
				if err := os.MkdirAll(aliasRoot, 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadAuthority(context.Background(), policy, key, []string{aliasRoot}); err != nil {
					t.Fatal("distinct filesystem name was case-folded", err)
				}
			}
			outsidePolicy, outsideKey := writeProtectedAuthority(t, filepath.Join(owner, "outside"), p, seed)
			if _, err := LoadAuthority(context.Background(), outsidePolicy, outsideKey, []string{realRoot}); err != nil {
				t.Fatal("healthy outside authority rejected", err)
			}
		})
	}
}

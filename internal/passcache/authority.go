package passcache

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
)

// Policy is supplied by the trusted invoking process, never by a restored
// cache or a source manifest. Signatures authenticate outcomes, not qualification.
type Policy struct {
	Schema     int      `json:"schema"`
	Namespace  string   `json:"namespace"`
	Epoch      uint64   `json:"epoch"`
	PublicKeys []string `json:"public_keys"`
}

// Authority separates immutable verification policy from optional issuance.
// Its private key is never serialized, transported or exposed to a guest.
type Authority struct {
	namespace string
	epoch     uint64
	keys      map[string]ed25519.PublicKey
	issuer    string
	private   ed25519.PrivateKey
	files     []protectedFile
}

type protectedFile struct {
	path, resolved, parentResolved string
	info, parent                   os.FileInfo
	data                           []byte
}

func NewAuthority(policy Policy, seed []byte) (*Authority, error) {
	if policy.Schema != 1 || policy.Epoch == 0 || len(policy.Namespace) == 0 || len(policy.Namespace) > 256 || len(policy.PublicKeys) == 0 || len(policy.PublicKeys) > 16 {
		return nil, errors.New("invalid portable pass policy")
	}
	for _, r := range policy.Namespace {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/-", r)) {
			return nil, errors.New("invalid portable pass namespace")
		}
	}
	a := &Authority{namespace: policy.Namespace, epoch: policy.Epoch, keys: make(map[string]ed25519.PublicKey)}
	for _, encoded := range policy.PublicKeys {
		if len(encoded) != ed25519.PublicKeySize*2 {
			return nil, errors.New("invalid portable pass public key")
		}
		key, err := hex.DecodeString(encoded)
		if err != nil || len(key) != ed25519.PublicKeySize || hex.EncodeToString(key) != encoded {
			return nil, errors.New("invalid portable pass public key")
		}
		id := publicKeyID(key)
		if _, exists := a.keys[id]; exists {
			return nil, errors.New("duplicate portable pass issuer")
		}
		a.keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	if seed != nil {
		if len(seed) != ed25519.SeedSize {
			return nil, errors.New("invalid portable pass signing key")
		}
		a.private = ed25519.NewKeyFromSeed(seed)
		a.issuer = publicKeyID(a.private.Public().(ed25519.PublicKey))
		if _, ok := a.keys[a.issuer]; !ok {
			return nil, errors.New("portable pass issuer is not trusted")
		}
	}
	return a, nil
}

func publicKeyID(key []byte) string {
	h := sha256.Sum256(key)
	return hex.EncodeToString(h[:])
}

func (a *Authority) CanIssue() bool { return a != nil && len(a.private) == ed25519.PrivateKeySize }

// LoadAuthority requires explicit absolute paths outside the checkout and
// cache, in private parent directories. These local controls do not establish
// hosted CI policy trust, distributed revocation or protection from the owner.
func LoadAuthority(ctx context.Context, policyPath, seedPath string, forbiddenRoots []string) (*Authority, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := captureProtectedFile(ctx, policyPath, 64<<10, forbiddenRoots)
	if err != nil {
		return nil, errors.Join(ctx.Err(), errors.New("portable pass policy unavailable"))
	}
	var policy Policy
	if config.DecodeValue(bytes.NewReader(p.data), &policy, 64<<10) != nil {
		return nil, errors.New("invalid portable pass policy")
	}
	files := []protectedFile{p}
	var seed []byte
	if seedPath != "" {
		f, e := captureProtectedFile(ctx, seedPath, ed25519.SeedSize, forbiddenRoots)
		if e != nil || f.info.Mode().Perm() != 0600 || len(f.data) != ed25519.SeedSize {
			return nil, errors.Join(ctx.Err(), errors.New("portable pass signing key unavailable"))
		}
		seed = f.data
		files = append(files, f)
	}
	a, err := NewAuthority(policy, seed)
	if err != nil {
		return nil, err
	}
	a.files = files
	return a, a.Verify(ctx)
}

func captureProtectedFile(ctx context.Context, path string, limit int64, forbidden []string) (protectedFile, error) {
	var f protectedFile
	if err := ctx.Err(); err != nil {
		return f, err
	}
	if len(path) > 4096 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return f, errors.New("protected authority path must be absolute")
	}
	info, e := os.Lstat(path)
	resolved, r := filepath.EvalSymlinks(path)
	parent, e2 := os.Lstat(filepath.Dir(path))
	parentResolved, r2 := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil || r != nil || e2 != nil || r2 != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return f, errors.New("protected authority file must be private")
	}
	for _, root := range forbidden {
		if root == "" {
			continue
		}
		absolute, e := filepath.Abs(root)
		if e != nil {
			return f, errors.New("authority exclusion unavailable")
		}
		rootInfo, e := os.Stat(absolute)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return f, errors.New("authority exclusion unavailable")
		}
		if e == nil {
			if !rootInfo.IsDir() {
				return f, errors.New("authority exclusion is not a directory")
			}
			for parent := filepath.Dir(resolved); ; parent = filepath.Dir(parent) {
				parentInfo, e := os.Stat(parent)
				if e != nil {
					return f, errors.New("authority ancestor unavailable")
				}
				if os.SameFile(rootInfo, parentInfo) {
					return f, errors.New("authority must be outside source and cache")
				}
				if filepath.Dir(parent) == parent {
					break
				}
			}
		}
		actual, e := filepath.EvalSymlinks(absolute)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return f, errors.New("authority exclusion unavailable")
		}
		if actual == "" {
			// Resolve the existing parent of a not-yet-created cache, including
			// every ancestor alias, without creating or trusting cache contents.
			actual = absolute
			var suffix []string
			for {
				v, e := filepath.EvalSymlinks(actual)
				if e == nil {
					actual = filepath.Join(append([]string{v}, suffix...)...)
					break
				}
				if !errors.Is(e, os.ErrNotExist) || filepath.Dir(actual) == actual {
					return f, errors.New("authority exclusion unavailable")
				}
				suffix = append([]string{filepath.Base(actual)}, suffix...)
				actual = filepath.Dir(actual)
			}
		}
		for _, candidate := range []string{path, resolved} {
			for _, base := range []string{absolute, actual} {
				rel, e := filepath.Rel(base, candidate)
				if e != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return f, errors.New("authority must be outside source and cache")
				}
			}
		}
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return f, err
	}
	defer root.Close()
	data, err := readPrivate(root, filepath.Base(path), limit)
	if err != nil {
		return f, err
	}
	f = protectedFile{path: path, resolved: resolved, parentResolved: parentResolved, info: info, parent: parent, data: data}
	return f, f.verify(ctx)
}

func (f protectedFile) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, e := os.Lstat(f.path)
	resolved, r := filepath.EvalSymlinks(f.path)
	parent, e2 := os.Lstat(filepath.Dir(f.path))
	parentResolved, r2 := filepath.EvalSymlinks(filepath.Dir(f.path))
	if e != nil || r != nil || e2 != nil || r2 != nil || resolved != f.resolved || parentResolved != f.parentResolved || !samePrivateFile(f.info, info) || !sameProtectedDirectory(f.parent, parent) {
		return errors.New("portable pass authority changed")
	}
	root, err := os.OpenRoot(filepath.Dir(f.path))
	if err != nil {
		return errors.New("portable pass authority changed")
	}
	defer root.Close()
	data, err := readPrivate(root, filepath.Base(f.path), int64(len(f.data)))
	if err != nil || !bytes.Equal(data, f.data) {
		return errors.New("portable pass authority changed")
	}
	info, e = os.Lstat(f.path)
	parent, e2 = os.Lstat(filepath.Dir(f.path))
	resolved, r = filepath.EvalSymlinks(f.path)
	parentResolved, r2 = filepath.EvalSymlinks(filepath.Dir(f.path))
	if e != nil || r != nil || e2 != nil || r2 != nil || resolved != f.resolved || parentResolved != f.parentResolved || !samePrivateFile(f.info, info) || !sameProtectedDirectory(f.parent, parent) {
		return errors.New("portable pass authority changed")
	}
	return ctx.Err()
}

func sameProtectedDirectory(a, b os.FileInfo) bool {
	return a != nil && b != nil && b.IsDir() && os.SameFile(a, b) && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}

func (a *Authority) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.namespace == "" || a.epoch == 0 || len(a.keys) == 0 {
		return errors.New("portable pass authority unavailable")
	}
	for _, f := range a.files {
		if err := f.verify(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

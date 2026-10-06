package passcache

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"time"
)

// Audit retains the selected cache root and key before request validation. It
// creates neither directories nor secrets; Observe can adopt the first cache
// actually opened by this invocation if none existed at admission.
type Audit struct {
	cache             *Cache
	directory, domain string
	authority         *Authority
	info, keyInfo     os.FileInfo
}

func BeginAudit(directory, domain string, authority *Authority) (*Audit, error) {
	a := &Audit{directory: directory, domain: domain, authority: authority}
	if directory == "" {
		return a, nil
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || domain == "" || len(domain) > 256 {
		return nil, errors.New("audit cache must be a private directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	a.cache = &Cache{root: root, domain: domain, authority: authority}
	a.info = info
	if authority == nil {
		secret, err := readPrivate(root, "authority.key", 32)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, errors.Join(errors.New("existing audit authority unavailable"), err, a.cache.Close())
		}
		if err == nil {
			if len(secret) != 32 {
				return nil, errors.Join(errors.New("existing audit authority unavailable"), a.cache.Close())
			}
			a.cache.secret = secret
			a.keyInfo, err = root.Lstat("authority.key")
			if err != nil {
				return nil, errors.Join(err, a.cache.Close())
			}
		}
	}
	if err := a.unchanged(); err != nil {
		return nil, errors.Join(err, a.cache.Close())
	}
	return a, nil
}

// Validate prevents an invocation from borrowing a handle for another provider,
// directory or authority. The handle is shared only within one serial audit.
func (a *Audit) Validate(directory, domain string, authority *Authority) error {
	if a == nil || a.directory != directory || a.domain != domain || a.authority != authority {
		return errors.New("audit handle does not match invocation")
	}
	return a.unchanged()
}

// Observe retains the actual cache opened by a provider, including a newly
// created root/key. Previously admitted identities are never replaced.
func (a *Audit) Observe(cache *Cache) error {
	if cache == nil {
		return nil
	}
	if a == nil || cache.domain != a.domain || cache.authority != a.authority {
		return errors.New("audit cache does not match invocation")
	}
	actual, err := cache.root.Stat(".")
	if err != nil {
		return err
	}
	if a.cache == nil {
		root, err := cache.root.OpenRoot(".")
		if err != nil {
			return err
		}
		a.cache = &Cache{root: root, domain: a.domain, authority: a.authority}
		a.info = actual
	} else if !os.SameFile(a.info, actual) || a.info.Mode() != actual.Mode() {
		return errors.New("audit cache location changed")
	}
	if a.authority == nil {
		if len(cache.secret) != 32 {
			return errors.New("audit cache authority unavailable")
		}
		if len(a.cache.secret) == 0 {
			a.cache.secret = append([]byte(nil), cache.secret...)
			a.keyInfo, err = a.cache.root.Lstat("authority.key")
			if err != nil {
				return err
			}
		} else if !bytes.Equal(a.cache.secret, cache.secret) {
			return errors.New("audit cache authority changed")
		}
	}
	return a.unchanged()
}

func (a *Audit) unchanged() error {
	if a == nil || a.cache == nil {
		return nil
	}
	named, err := os.Lstat(a.directory)
	if err != nil || !named.IsDir() || !os.SameFile(a.info, named) || named.Mode() != a.info.Mode() {
		return errors.New("audit cache location changed")
	}
	retained, err := a.cache.root.Stat(".")
	if err != nil || !os.SameFile(a.info, retained) || retained.Mode() != a.info.Mode() {
		return errors.New("audit cache location changed")
	}
	if a.authority == nil && a.keyInfo != nil {
		info, err := a.cache.root.Lstat("authority.key")
		if err != nil || !samePrivateFile(a.keyInfo, info) {
			return errors.New("audit cache authority changed")
		}
		key, err := readPrivate(a.cache.root, "authority.key", 32)
		if err != nil || !bytes.Equal(a.cache.secret, key) {
			return errors.New("audit cache authority changed")
		}
	}
	return nil
}

func (a *Audit) Revoke() error {
	if a == nil || a.cache == nil || (a.authority == nil && len(a.cache.secret) == 0) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.cache.RevokeAll(ctx)
}

// Finish returns drift and cleanup errors while revoking the retained root on
// failure. It never reopens a renamed cache or adopts a replacement key.
func (a *Audit) Check(failed bool) error {
	if a == nil || a.cache == nil {
		return nil
	}
	drift := a.unchanged()
	var revoke error
	if failed || drift != nil {
		revoke = a.Revoke()
	}
	return errors.Join(drift, revoke)
}

func (a *Audit) Finish(failed bool) error {
	if a == nil || a.cache == nil {
		return nil
	}
	return errors.Join(a.Check(failed), a.cache.Close())
}

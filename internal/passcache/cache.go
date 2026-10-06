// Package passcache stores bounded, locally authenticated results. Callers own
// qualification and identity checks; authentication never qualifies execution.
package passcache

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"syscall"
)

const MaxBytes = 1 << 20

type Cache struct {
	root   *os.Root
	secret []byte
	domain string
}
type envelope struct {
	Schema int             `json:"schema"`
	Domain string          `json:"domain"`
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	MAC    string          `json:"mac"`
}

func Open(directory, domain string) (*Cache, error) {
	if directory == "" {
		return nil, nil
	}
	if runtime.GOOS == "windows" || domain == "" || len(domain) > 256 {
		return nil, errors.New("pass cache authority unsupported")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, errors.New("pass cache unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("pass cache must be private")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	c := &Cache{root: root, domain: domain}
	secret, err := readPrivate(root, "authority.key", 32)
	if errors.Is(err, fs.ErrNotExist) {
		secret = make([]byte, 32)
		_, err = rand.Read(secret)
		if err == nil {
			f, e := root.OpenFile("authority.key", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e == nil {
				_, w := f.Write(secret)
				s := f.Sync()
				closeErr := f.Close()
				if w != nil || s != nil || closeErr != nil {
					err = errors.New("pass authority persistence failed")
				}
			} else if errors.Is(e, fs.ErrExist) {
				secret, err = readPrivate(root, "authority.key", 32)
			} else {
				err = e
			}
		}
	}
	if err != nil || len(secret) != 32 {
		root.Close()
		return nil, errors.New("pass authority unavailable")
	}
	c.secret = secret
	return c, nil
}
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.root.Close()
}
func validKey(key string) bool {
	data, err := hex.DecodeString(key)
	return err == nil && len(data) == 32 && strings.ToLower(key) == key
}
func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if limit < 0 || limit > MaxBytes || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("pass object must be private and bounded")
	}
	return readPrivateEntry(root, name, info)
}

// Keep opening nonblocking even when a FIFO replaces the preflight file. The
// admitted size bounds allocation; inode and full metadata drift reject reuse.
func readPrivateEntry(root *os.Root, name string, info os.FileInfo) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !samePrivateFile(info, opened) || !samePrivateFile(info, current) {
		return nil, errors.New("pass object changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil {
		return nil, errors.New("pass object exceeds bound")
	}
	if err := validatePrivateRead(root, name, f, info, int64(len(data))); err != nil {
		return nil, err
	}
	return data, nil
}

func validatePrivateRead(root *os.Root, name string, f *os.File, before os.FileInfo, read int64) error {
	after, err := f.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || read != before.Size() || !samePrivateFile(before, after) || !samePrivateFile(before, current) {
		return errors.New("pass object changed")
	}
	return nil
}

func samePrivateFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && after.Mode().IsRegular() && os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
func (c *Cache) authenticate(e envelope) []byte {
	e.MAC = ""
	data, _ := json.Marshal(e)
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte("hayaku-local-pass-v2\x00"))
	mac.Write(data)
	return mac.Sum(nil)
}
func (c *Cache) Load(key string) ([]byte, string) {
	if c == nil {
		return nil, "disabled"
	}
	if !validKey(key) {
		return nil, "invalid"
	}
	data, err := readPrivate(c.root, key+".json", MaxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "miss"
	}
	if err != nil {
		return nil, "invalid"
	}
	var e envelope
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF {
		return nil, "invalid"
	}
	canonical, _ := json.Marshal(e)
	provided, err := hex.DecodeString(e.MAC)
	if !bytes.Equal(data, canonical) || e.Schema != 2 || e.Domain != c.domain || e.Key != key || err != nil || !hmac.Equal(provided, c.authenticate(e)) {
		return nil, "invalid"
	}
	return e.Value, "hit"
}
func (c *Cache) Invalidate(key string) error {
	if c == nil {
		return nil
	}
	if !validKey(key) {
		return errors.New("invalid pass identity")
	}
	err := c.root.Remove(key + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
func (c *Cache) Store(ctx context.Context, key string, value any) error {
	if c == nil {
		return nil
	}
	if !validKey(key) {
		return errors.New("invalid pass identity")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	e := envelope{Schema: 2, Domain: c.domain, Key: key, Value: raw}
	e.MAC = hex.EncodeToString(c.authenticate(e))
	data, _ := json.Marshal(e)
	if len(data) > MaxBytes {
		return errors.New("pass receipt exceeds bound")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	name := ".pass-" + hex.EncodeToString(random)
	f, err := c.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer c.root.Remove(name)
	_, w := f.Write(data)
	s := f.Sync()
	closeErr := f.Close()
	if w != nil || s != nil || closeErr != nil {
		return errors.New("pass persistence failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.root.Rename(name, key+".json"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = c.Invalidate(key)
		return err
	}
	return nil
}

// RevokeAll removes authenticated receipts owned by this contract before an
// invocation-wide audit starts. Other domains sharing the directory are kept.
// This also covers cancellation or build failures before a group key exists.
func (c *Cache) RevokeAll(ctx context.Context) error {
	if c == nil {
		return nil
	}
	directory, err := c.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			if err = ctx.Err(); err != nil {
				return err
			}
			key := strings.TrimSuffix(name, ".json")
			if key == name || !validKey(key) {
				continue
			}
			if _, status := c.Load(key); status == "hit" {
				if err = c.Invalidate(key); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// Package evidence stores private, advisory metadata; cache hits cannot authorize skips.
package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
)

type envelope struct {
	Schema   int            `json:"schema"`
	Key      string         `json:"key"`
	Digest   string         `json:"digest"`
	Evidence model.Evidence `json:"evidence"`
}

func Load(dir, key string) (model.Evidence, bool) {
	var value model.Evidence
	parent, err := os.Lstat(dir)
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return value, false
	}
	if len(key) != 64 || filepath.Base(key) != key {
		return value, false
	}
	path := filepath.Join(dir, key+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<20 {
		return value, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return value, false
	}
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil || e.Schema != model.Schema || e.Key != key || e.Digest != config.Digest(e.Evidence) {
		return value, false
	}
	return e.Evidence, true
}

func Store(dir, key string, value model.Evidence) error {
	if len(key) != 64 || filepath.Base(key) != key {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return os.ErrPermission
	}
	b, err := json.Marshal(envelope{model.Schema, key, config.Digest(value), value})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".part-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, key+".json"))
}

package evidence

import (
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestCorruptCacheCannotReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	key := config.Digest("key")
	e := model.Evidence{Adapter: "go", Gaps: []model.Gap{{Code: "runtime-unqualified", Detail: "keep full"}}}
	if err := Store(dir, key, e); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(dir, key); !ok {
		t.Fatal("cache miss")
	}
	path := filepath.Join(dir, key+".json")
	if err := os.WriteFile(path, []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(dir, key); ok {
		t.Fatal("corrupt hit")
	}
	if err := Store(dir, key, e); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0644)
	if _, ok := Load(dir, key); ok {
		t.Fatal("public cache hit")
	}
}

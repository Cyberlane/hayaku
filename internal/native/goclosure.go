package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/model"
)

// GoClosure owns an immutable private source materialization and descriptive
// compiler/type evidence. It does not qualify imported Go packages or replace
// the existing native.Go certifier. Close it after the later certifier/build.
type GoClosure struct {
	Evidence                                golang.Closure
	SourceDigest                            string
	root, source, temporary, evidenceDigest string
}

func CaptureGoClosure(ctx context.Context, root string, workspace model.Workspace, execution model.Context) (_ *GoClosure, err error) {
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, errors.New("Go closure root unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Go closure root must be a real directory")
	}
	temporary, err := os.MkdirTemp("", "hayaku-go-closure-")
	if err != nil {
		return nil, errors.New("Go closure materialization unavailable")
	}
	c := &GoClosure{root: root, source: filepath.Join(temporary, "source"), temporary: temporary}
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	if err = os.Mkdir(c.source, 0700); err != nil {
		return nil, errors.New("Go closure materialization unavailable")
	}
	c.SourceDigest, err = capture(ctx, root, c.source)
	if err != nil {
		return nil, err
	}
	c.Evidence, err = golang.ResolveClosure(ctx, c.source, workspace, execution)
	if err != nil {
		return nil, err
	}
	c.evidenceDigest = c.Evidence.Digest
	if err = c.Revalidate(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Directory is a private build path, never report evidence or a runner context
// equivalence claim. Only trusted in-process builders should use it.
func (c *GoClosure) Directory() string {
	if c == nil {
		return ""
	}
	return c.source
}

func (c *GoClosure) Revalidate(ctx context.Context) error {
	if c == nil || c.temporary == "" || c.Evidence.Digest != c.evidenceDigest {
		return errors.New("Go closure capture unavailable or changed")
	}
	current, err := treeIdentity(ctx, c.root)
	if err != nil || current != c.SourceDigest {
		return errors.New("Go closure live source changed")
	}
	return golang.RevalidateClosure(ctx, c.source, c.Evidence)
}

func (c *GoClosure) Close() error {
	if c == nil || c.temporary == "" {
		return nil
	}
	err := os.RemoveAll(c.temporary)
	if err == nil {
		c.temporary = ""
		c.source = ""
	}
	return err
}

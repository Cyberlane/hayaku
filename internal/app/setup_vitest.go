package app

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
)

// detectVitest reads manifest data only. npm scripts are shell programs and are
// deliberately not parsed into commands; the generated run scope needs review.
func detectVitest(root string) (model.Workspace, bool) {
	f, err := os.Open(filepath.Join(root, "package.json"))
	if err != nil {
		return model.Workspace{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, config.MaxBytes+1))
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
		Development  map[string]string `json:"devDependencies"`
	}
	if err != nil || len(data) > config.MaxBytes || json.Unmarshal(data, &manifest) != nil || (manifest.Dependencies["vitest"] == "" && manifest.Development["vitest"] == "") {
		return model.Workspace{}, false
	}
	w := model.Workspace{ID: "vitest", Root: ".", Adapter: "vitest", Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}}
	modules := filepath.Join(root, "node_modules")
	info, err := os.Lstat(modules)
	node, nodeErr := exec.LookPath("node")
	if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && nodeErr == nil {
		node, err = filepath.Abs(node)
		if err == nil {
			w.NodeRuntime = &model.NodeRuntime{Node: node, Modules: modules}
			name := "vitest"
			if filepath.Separator == '\\' {
				name += ".cmd"
			}
			w.Command.Executable = filepath.Join("node_modules", ".bin", name)
		}
	}
	return w, true
}

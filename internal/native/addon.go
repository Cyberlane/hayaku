package native

import (
	"context"
	"debug/elf"
	"errors"
	"path/filepath"
	"sort"
)

const runtimeAddonLimit = 16

// RuntimeAddon declares captured native code in an operational runtime. It does
// not qualify user FFI or frontend effects, or issue provider proofs or receipts.
type RuntimeAddon struct{ Name, Path string }

func addonName(name string) bool       { return len(name) <= 128 && dynamicName(name) }
func addonFileName(name string) string { return "#addon-" + name + ".node" }

func preflightAddons(request RuntimeRequest, text int) error {
	if len(request.Addons) > runtimeAddonLimit || len(request.Libraries)+len(request.Addons) > dynamicLibraryLimit {
		return errors.New("dynamic addon declaration exceeds role bounds")
	}
	names := map[string]bool{}
	for _, addon := range request.Addons {
		text += len(addon.Name) + len(addon.Path)
		if !addonName(addon.Name) || names[addon.Name] || !dynamicText(addon.Path) || !filepath.IsAbs(addon.Path) || text > 1<<20 {
			return errors.New("invalid dynamic addon declaration")
		}
		names[addon.Name] = true
	}
	return nil
}

func addonNames(addons map[string]*dynamicFile) []string {
	names := make([]string, 0, len(addons))
	for name := range addons {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Runtime) validateAddons() error {
	if len(r.addons) > runtimeAddonLimit {
		return errors.New("dynamic addon identity exceeds role bounds")
	}
	sonames := map[string]bool{r.loaderName(): true}
	for name := range r.libraries {
		sonames[name] = true
	}
	for _, name := range addonNames(r.addons) {
		f := r.addons[name]
		if !addonName(name) || f == nil || f.image.kind != uint16(elf.ET_DYN) || f.image.interpreter != "" || f.image.architecture != r.loader.image.architecture {
			return errors.New("dynamic addon role or architecture mismatch")
		}
		if f.image.soname != "" {
			if !dynamicName(f.image.soname) || sonames[f.image.soname] {
				return errors.New("dynamic addon SONAME collision")
			}
			sonames[f.image.soname] = true
		}
	}
	return nil
}

// AddonPath returns a verified operational transport path. The native caller
// must still use CaptureDynamicProgram and its complete borrowed runtime.
func (r *Runtime) AddonPath(ctx context.Context, name string) (string, error) {
	if !addonName(name) {
		return "", errors.New("invalid dynamic addon name")
	}
	if err := r.Verify(ctx); err != nil {
		return "", err
	}
	if _, found := r.addons[name]; !found {
		return "", errors.New("dynamic addon is not captured")
	}
	return "/runtime/" + addonFileName(name), nil
}

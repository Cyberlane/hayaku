package cmake

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/adapter/buildclosure"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
)

// Request admits an installed CMake/Ninja/C++ build, with explicitly supplied
// environment, SDK/framework inputs and bounded -D cache definitions. Native
// configure/build hooks execute trusted project code outside containment.
type Request struct {
	Source, Revision              string
	CMake, Ninja, Compiler, CTest string
	CCompiler                     string
	Environment, Definitions      []string
	Inputs                        []inputbundle.Request
}

type Closure struct {
	mu      sync.Mutex
	session *buildclosure.Session
	request Request
	api     FileAPI
}

// Build uses a fresh source copy and build tree. It neither installs tools nor
// resolves missing dependencies. Offline flags are build settings, not a
// network enforcement claim.
func Build(ctx context.Context, request Request) (_ *Closure, err error) {
	if request.Environment == nil {
		return nil, errors.New("explicit CMake environment required")
	}
	if len(request.Environment) > 4096 || bounded(request.Definitions, 1024, 4096) != nil || bounded(request.Environment, 4096, 32768) != nil {
		return nil, errors.New("CMake request exceeds admission bounds")
	}
	request.Environment = append([]string{}, request.Environment...)
	request.Definitions = append([]string(nil), request.Definitions...)
	reserved := map[string]bool{"CMAKE_HOME_DIRECTORY": true, "CMAKE_CACHEFILE_DIR": true, "CMAKE_MAKE_PROGRAM": true, "CMAKE_CXX_COMPILER": true, "CMAKE_C_COMPILER": true, "FETCHCONTENT_FULLY_DISCONNECTED": true, "FETCHCONTENT_UPDATES_DISCONNECTED": true, "CMAKE_TOOLCHAIN_FILE": true, "CMAKE_PROJECT_TOP_LEVEL_INCLUDES": true}
	seen := map[string]bool{}
	for _, definition := range request.Definitions {
		key, _, ok := strings.Cut(definition, "=")
		key, _, _ = strings.Cut(key, ":")
		if !ok || !identity(definition) || key == "" || reserved[key] || seen[key] {
			return nil, errors.New("invalid or reserved CMake definition")
		}
		seen[key] = true
	}
	tools := []string{request.CMake, request.Ninja, request.Compiler}
	if request.CCompiler != "" {
		tools = append(tools, request.CCompiler)
	}
	if request.CTest != "" {
		tools = append(tools, request.CTest)
	}
	session, err := buildclosure.New(ctx, buildclosure.Request{Source: request.Source, Revision: request.Revision, Tools: tools, Environment: request.Environment, Files: queryFiles()})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, session.Close())
		}
	}()
	for _, inputs := range request.Inputs {
		if err = session.BindInputs(ctx, inputs); err != nil {
			return nil, err
		}
	}
	args := []string{"-S", "{source}", "-B", "{build}", "-G", "Ninja", "-DCMAKE_MAKE_PROGRAM=" + request.Ninja, "-DCMAKE_CXX_COMPILER=" + request.Compiler, "-DFETCHCONTENT_FULLY_DISCONNECTED=ON", "-DFETCHCONTENT_UPDATES_DISCONNECTED=ON"}
	if request.CCompiler != "" {
		args = append(args, "-DCMAKE_C_COMPILER="+request.CCompiler)
	}
	for _, definition := range request.Definitions {
		args = append(args, "-D"+definition)
	}
	out, err := session.RunProducer(ctx, "configure", model.Command{Executable: request.CMake, Args: args, Dir: "{build}"})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return nil, errors.Join(err, errors.New("CMake configure failed; diagnostics withheld"))
	}
	configured, err := CollectFileAPI(ctx, session)
	if err != nil {
		return nil, err
	}
	if err = admitCompilers(ctx, session, request, configured); err != nil {
		return nil, err
	}
	out, err = session.RunProducer(ctx, "build", model.Command{Executable: request.Ninja, Args: []string{"-C", "{build}", "-j1"}, Dir: "{build}"})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return nil, errors.Join(err, errors.New("Ninja build failed; diagnostics withheld"))
	}
	api, err := CollectFileAPI(ctx, session)
	if err != nil {
		return nil, err
	}
	if err = admitCompilers(ctx, session, request, api); err != nil {
		return nil, err
	}
	api, err = observeDependencies(ctx, session, request.Ninja, api)
	if err != nil {
		return nil, err
	}
	return &Closure{session: session, request: request, api: api}, nil
}

func observeDependencies(ctx context.Context, session *buildclosure.Session, ninja string, api FileAPI) (FileAPI, error) {
	out, err := session.Run(ctx, model.Command{Executable: ninja, Dir: "{build}", Args: []string{"-C", "{build}", "-t", "deps"}})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return FileAPI{}, errors.Join(err, errors.New("Ninja dependency collection failed"))
	}
	api.HeaderInputs, err = ninjaHeaders(out.Stdout, session.Source(), session.Build())
	if err != nil {
		return FileAPI{}, err
	}
	if len(api.HeaderInputs) == 0 {
		api.Gaps = append(api.Gaps, model.Gap{Code: "ninja-dependencies-incomplete", Detail: "No compiler dependency records were observed; absence is not evidence of absent header influence."})
	}
	_, binding, err := session.Records(ctx)
	if err != nil {
		return FileAPI{}, err
	}
	api.Digest = digest([]any{api.Digest, binding, digest(out.Stdout), digest(out.Stderr), api.HeaderInputs})
	return api, nil
}

// Observation returns a defensive logical projection, never physical SDK paths.
func (c *Closure) Observation(ctx context.Context) (FileAPI, error) {
	if c == nil || c.session == nil {
		return FileAPI{}, errors.New("missing CMake closure")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api.Digest == "" {
		return FileAPI{}, errors.New("configured CMake generation unavailable")
	}
	_, binding, err := c.session.Records(ctx)
	if err != nil {
		return FileAPI{}, err
	}
	r := c.api
	r.Digest = digest([]string{r.Digest, binding})
	r.compilers = nil
	r.Gaps = append([]model.Gap(nil), r.Gaps...)
	r.HeaderInputs = append([]string(nil), r.HeaderInputs...)
	r.Targets = append([]Target(nil), r.Targets...)
	for i := range r.Targets {
		t := &r.Targets[i]
		t.Sources = append([]string(nil), t.Sources...)
		t.Generated = append([]string(nil), t.Generated...)
		t.Artifacts = append([]string(nil), t.Artifacts...)
		t.Dependencies = append([]string(nil), t.Dependencies...)
	}
	return r, nil
}

func bounded(values []string, count, perValue int) error {
	if len(values) > count {
		return errors.New("CMake option count exceeds bound")
	}
	bytes := 0
	for _, value := range values {
		bytes += len(value)
		if value == "" || len(value) > perValue || bytes > 1<<20 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("CMake options exceed text/aggregate bounds")
		}
	}
	return nil
}

func admitCompilers(ctx context.Context, session *buildclosure.Session, request Request, api FileAPI) error {
	if len(api.compilers) == 0 {
		return errors.New("configured compiler inventory missing")
	}
	for language, reported := range api.compilers {
		expected := ""
		switch language {
		case "CXX":
			expected = request.Compiler
		case "C":
			expected = request.CCompiler
		}
		if expected == "" {
			return errors.New("configured language compiler not explicitly admitted")
		}
		resolved, err := filepath.EvalSymlinks(expected)
		if err != nil || (reported != expected && reported != resolved) {
			return errors.New("configured compiler contradicts bound invocation")
		}
		if err = session.CaptureTool(ctx, reported); err != nil {
			return err
		}
	}
	return nil
}

func (c *Closure) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session.Close()
}

func (c *Closure) executable(ctx context.Context, id string) (string, error) {
	if c == nil || c.session == nil {
		return "", errors.New("missing CMake closure")
	}
	for _, target := range c.api.Targets {
		if target.ID != id {
			continue
		}
		if target.Kind != "EXECUTABLE" || len(target.Artifacts) != 1 {
			return "", errors.New("target is not one native executable")
		}
		name := filepath.Join(c.session.Build(), filepath.FromSlash(target.Artifacts[0]))
		if err := c.session.CaptureArtifact(ctx, name); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("unknown CMake executable target")
}

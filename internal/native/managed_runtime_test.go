package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestManagedCompilerProfileRequiresPrivateCapture(t *testing.T) {
	r, strict, _ := dynamicFixture(t)
	if strict.profile != "" {
		t.Fatal("public capture admitted managed compiler profile")
	}
	request := DynamicProgramRequest{Program: strict.primary.source, Args: []string{"fixed"}, Runtime: r}
	if p, err := captureDynamicProgram(context.Background(), request, "caller-asserted"); err == nil || p != nil {
		t.Fatal("unknown compiler profile admitted")
	}
	p, err := captureManagedCompilerProgram(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	request.Args[0] = "caller mutation"
	if p.profile != managedCompilerProfile || p.args[0] != "fixed" || p.Verify(context.Background()) != nil {
		t.Fatal("private compiler capture lost its bound profile or args")
	}
	for _, mutation := range []string{"", "unknown"} {
		p.profile = mutation
		if err := p.Verify(context.Background()); err == nil {
			t.Fatal("compiler profile mutation admitted", mutation)
		}
		p.profile = managedCompilerProfile
	}
	strict.profile = managedCompilerProfile
	if err := strict.Verify(context.Background()); err == nil {
		t.Fatal("strict program upgraded through private field mutation")
	}
	strict.profile = ""
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("independent compiler cancellation")
	cancel(cause)
	if p, err := captureManagedCompilerProgram(ctx, request); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled compiler capture admitted", err)
	}
}

func TestManagedCompilerProfileBindsTransportAndRejectsBeforeLaunch(t *testing.T) {
	r, strict, _ := dynamicFixture(t)
	p, err := captureManagedCompilerProgram(context.Background(), DynamicProgramRequest{Program: strict.primary.source, Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	strictContract := dynamicInvocation{Loader: r.loaderName(), ResourcesDigest: strings.Repeat("a", 64)}
	managedContract := strictContract
	managedContract.CompilerProfile = p.profile
	a, _ := json.Marshal(strictContract)
	b, _ := json.Marshal(managedContract)
	if digest(a) == digest(b) || strings.Contains(string(a), "compiler_profile") || !strings.Contains(string(b), managedCompilerProfile) {
		t.Fatal("compiler profile is absent from retained transport identity")
	}
	// Neither a usable envelope nor a backend is needed: profile admission must
	// reject before either can be consulted or any process can be started.
	e := &Envelope{}
	p.profile = ""
	out, err := RunDynamicProgram(context.Background(), Backend{Platform: "linux"}, p, e)
	if err == nil || out.Completed || len(out.Stdout) != 0 || len(out.Stderr) != 0 || !strings.Contains(err.Error(), "private identity") {
		t.Fatal("changed compiler profile reached backend", err)
	}
	p.profile = managedCompilerProfile
}

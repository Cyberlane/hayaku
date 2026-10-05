package native

import (
	"testing"
)

func TestPureProofChecksUnexecutedCode(t *testing.T) {
	good := `package sample
 import "testing"
 func Add(a,b int)int{return a+b}
 func TestAdd(t *testing.T){if Add(2,3)!=5{t.Fatal("wrong")}}
 `
	if cases, err := qualifyGo(map[string][]byte{"sample_test.go": []byte(good)}); err != nil || len(cases) != 1 {
		t.Fatalf("pure proof: %v %v", cases, err)
	}
	scenarios := map[string]string{
		"clock":              `import "time";func Uncalled()int64{return time.Now().UnixNano()}`,
		"file":               `import "os";func Uncalled(){os.ReadFile("fixture")}`,
		"entropy":            `import "math/rand";func Uncalled()int{return rand.Int()}`,
		"network":            `import "net";func Uncalled(){net.Dial("tcp","localhost:1")}`,
		"reflection":         `import "reflect";func Uncalled(){reflect.ValueOf(1)}`,
		"process":            `import "os/exec";func Uncalled(){exec.Command("true").Run()}`,
		"global":             `var counter int`,
		"init":               `func init(){}`,
		"goroutine":          `func Uncalled(){go Add(1,2)}`,
		"defer":              `func Uncalled(){defer Add(1,2)}`,
		"function-value":     `func Uncalled(){f:=Add;_ = f}`,
		"function-argument":  `func Uncalled(t *testing.T){t.Error(Add)}`,
		"address":            `func Uncalled(){x:=1;_ = &x}`,
		"interface":          `type Value interface{Read()}`,
		"map":                `func Uncalled(){_ = map[string]int{}}`,
		"compiler-directive": "//go:noinline\nfunc Uncalled(){}",
	}
	for name, extra := range scenarios {
		t.Run(name, func(t *testing.T) {
			files := map[string][]byte{"sample_test.go": []byte(good), "extra.go": []byte("package sample\n" + extra)}
			if _, err := qualifyGo(files); err == nil {
				t.Fatal("unsafe proof accepted")
			}
		})
	}
	for _, body := range []string{`t.Parallel()`, `t.TempDir()`, `t.Setenv("A","B")`, `t.Run("sub",func(t *testing.T){})`, `t.Deadline()`, `f:=t.Name;_ = f`, `t.Error(t.Name)`, `other:=t;_ = other`, `t=nil`, `t.Logf("%p",[]byte{})`} {
		t.Run(body, func(t *testing.T) {
			source := `package sample;import "testing";func TestOne(t *testing.T){` + body + `}`
			if _, err := qualifyGo(map[string][]byte{"sample_test.go": []byte(source)}); err == nil {
				t.Fatal("unsupported testing behavior accepted")
			}
		})
	}
}
func TestProofAcceptsScalarAndSliceComputation(t *testing.T) {
	files := map[string][]byte{
		"sample.go":      []byte(`package sample;const factor=2;func Sum(values []int)int{total:=0;for _,v:=range values{total+=v};return total*factor}`),
		"sample_test.go": []byte(`package sample;import "testing";func TestSum(t *testing.T){if Sum([]int{1,2,3})!=12{t.Fatalf("wrong sum")}}`),
	}
	if _, err := qualifyGo(files); err != nil {
		t.Fatal(err)
	}
}

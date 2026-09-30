package report

import (
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestWireComparisonPreservesOptionalFieldsAndRejectsChangedCommands(t *testing.T) {
	a := model.Plan{Reasons: []model.Reason{{Unit: "a", Code: "changed-input", Via: []string{}}}, Commands: []model.Command{{Executable: "go", Args: []string{"test", "./..."}}}}
	b := a
	b.Reasons = []model.Reason{{Unit: "a", Code: "changed-input"}}
	if !Equal(a, b) || len(Differences(a, b)) != 0 {
		t.Fatal("equivalent optional wire fields rejected")
	}
	b.Commands = []model.Command{{Executable: "go", Args: []string{"test", "./a"}}}
	if Equal(a, b) || !reflect.DeepEqual(Differences(a, b), []string{"commands"}) {
		t.Fatal("changed command was accepted or its values leaked through diagnostic")
	}
}

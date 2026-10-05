package app

import "github.com/Cyberlane/hayaku/internal/model"

// commandOutcomes accepts existing in-memory fixtures and historical reports.
// New execution always uses the runner bridge's normalized result.
func commandOutcomes(command CommandResult) model.Outcomes {
	if command.Outcomes != nil {
		return *command.Outcomes
	}
	common := model.Outcomes{Complete: command.Complete}
	if command.Native != nil {
		common.Failed, common.Errors = command.Native.Failed, command.Native.UnhandledErrors
		for _, item := range command.Native.Units {
			common.Units = append(common.Units, model.Outcome{Unit: item.Unit, Action: item.Action})
		}
		for _, item := range command.Native.Tests {
			common.Tests = append(common.Tests, model.Outcome{Unit: item.Unit, Test: item.Test, Action: item.Action})
		}
	}
	if command.Vitest != nil {
		common.Failed, common.Errors = command.Vitest.Failed, command.Vitest.UnhandledErrors
		for _, item := range command.Vitest.Files {
			common.Units = append(common.Units, model.Outcome{Unit: item.Unit, Action: item.Action})
		}
		for _, item := range command.Vitest.Tests {
			common.Tests = append(common.Tests, model.Outcome{Unit: item.Unit, Test: item.Test, Action: item.Action})
		}
	}
	if command.Go != nil {
		common.Failed = command.Go.Failed
		for _, item := range command.Go.Packages {
			common.Units = append(common.Units, model.Outcome{Unit: command.Workspace + ":" + item.Package, Action: item.Action})
		}
		for _, item := range command.Go.Tests {
			common.Tests = append(common.Tests, model.Outcome{Unit: command.Workspace + ":" + item.Package, Test: item.Test, Action: item.Action})
		}
	}
	return common
}

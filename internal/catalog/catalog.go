// Package catalog describes runner integration independently of omission authority.
package catalog

import "sort"

type Runner struct {
	ID          string `json:"id"`
	Language    string `json:"language"`
	Framework   string `json:"framework"`
	Tool        string `json:"tool"`
	Discovery   string `json:"discovery"`
	Results     string `json:"results"`
	Granularity string `json:"granularity"`
	Assurance   string `json:"assurance"`
}

// Runners is a capability catalog, not a claim that every installed framework
// version or platform has passed native qualification. Full commands stay intact.
func Runners() []Runner {
	r := []Runner{
		{"go", "Go", "testing", "go", "native", "go-json", "package", "native-runtime-unqualified"},
		{"vitest", "JavaScript/TypeScript", "Vitest", "vitest", "native", "vitest-json", "file/project", "native-runtime-unqualified"},
		{"node-test", "JavaScript/TypeScript", "node:test", "node", "native", "native-json", "file", "native-runtime-unqualified"},
		{"jest", "JavaScript/TypeScript", "Jest", "jest", "native", "native-json", "file", "native-runtime-unqualified"},
		{"playwright", "JavaScript/TypeScript", "Playwright Test", "playwright", "native", "native-json", "file/project", "browser-services-unqualified"},
		{"pytest", "Python", "pytest", "python3", "native", "native-json", "file", "native-runtime-unqualified"},
		{"unittest", "Python", "unittest", "python3", "native", "native-json", "module", "native-runtime-unqualified"},
		{"cargo", "Rust", "libtest/Cargo", "cargo", "native", "exit-status", "package", "native-runtime-unqualified"},
		{"nextest", "Rust", "cargo-nextest", "cargo", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"maven", "Java/Kotlin", "JUnit/TestNG via Surefire/Failsafe", "mvn", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"gradle", "Java/Kotlin", "JUnit/TestNG", "gradle", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"sbt", "Scala", "ScalaTest/MUnit", "sbt", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"dotnet", "C#/F#", "xUnit/NUnit/MSTest", "dotnet", "full-scope", "trx/junit", "suite", "native-runtime-unqualified"},
		{"swift", "Swift", "Swift Testing/XCTest", "swift", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"xcode", "Swift/Objective-C", "XCTest/Swift Testing", "xcodebuild", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"rspec", "Ruby", "RSpec", "rspec", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"minitest", "Ruby", "Minitest", "ruby", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"phpunit", "PHP", "PHPUnit", "phpunit", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"dart", "Dart", "package:test", "dart", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"flutter", "Dart", "flutter_test", "flutter", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"gtest", "C/C++", "GoogleTest", "", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"catch2", "C/C++", "Catch2", "", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"ctest", "C/C++", "CTest", "ctest", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"bun", "JavaScript/TypeScript", "bun:test", "bun", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"deno", "JavaScript/TypeScript", "Deno.test", "deno", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"bazel", "multiple", "Bazel test rules", "bazel", "full-scope", "junit", "suite", "native-runtime-unqualified"},
		{"command", "multiple", "original configured runner", "", "full-scope", "exit-status", "suite", "native-runtime-unqualified"},
	}
	sort.Slice(r, func(i, j int) bool { return r[i].ID < r[j].ID })
	return r
}

func Lookup(id string) (Runner, bool) {
	for _, r := range Runners() {
		if r.ID == id {
			return r, true
		}
	}
	return Runner{}, false
}

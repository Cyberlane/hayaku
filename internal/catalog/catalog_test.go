package catalog

import "testing"

func TestCatalogKeepsLanguageRunnerAndAssuranceSeparate(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Runners() {
		if seen[r.ID] || r.ID == "" || r.Language == "" || r.Framework == "" || r.Assurance == "" {
			t.Fatalf("invalid catalog entry %#v", r)
		}
		seen[r.ID] = true
		if r.Assurance == "qualified" {
			t.Fatal("catalog cannot declare arbitrary native contexts qualified")
		}
	}
	for _, id := range []string{"node-test", "unittest", "jest", "playwright", "nextest", "maven", "gradle", "sbt", "dotnet", "swift", "xcode", "rspec", "minitest", "phpunit", "dart", "flutter", "gtest", "catch2", "ctest", "bun", "deno", "bazel"} {
		if _, ok := Lookup(id); !ok {
			t.Fatalf("missing requested runner %s", id)
		}
	}
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("unknown runner admitted")
	}
}

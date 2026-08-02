package featureflip

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// modulePathBase is the module path without any major-version suffix. It is
// also the public mirror repo (canopy-labs/featureflip-go); the /vN suffix
// lives in go.mod and the import path, never in the repo name.
const modulePathBase = "github.com/canopy-labs/featureflip-go"

var (
	moduleDirectiveRE  = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)
	changelogHeadingRE = regexp.MustCompile(`(?m)^##\s+v?(\d+)\.(\d+)\.(\d+)`)
	majorSuffixRE      = regexp.MustCompile(`/v\d+$`)
)

// TestModulePathMatchesReleasedMajor pins go.mod's module path to the major
// version we actually ship.
//
// Go requires a v2+ module to carry its major version in the module path. The
// proxy enforces this at fetch time, not build time, so a missing suffix does
// not fail any build — it makes every tag of that major unresolvable while an
// older major keeps serving:
//
//	not found: github.com/canopy-labs/featureflip-go@v2.4.0: invalid version:
//	module contains a go.mod file, so module path must match major version
//	("github.com/canopy-labs/featureflip-go/v2")
//
// That is exactly how v2.0.0 through v2.4.0 shipped while `go get` kept handing
// out v1.0.1 (#2138). Building the module in-tree cannot catch it, because
// in-tree the path is self-consistent whatever it says. This test compares the
// declared path against the CHANGELOG's newest release instead, so the mismatch
// surfaces on the PR that introduces it.
//
// See https://go.dev/ref/mod#major-version-suffixes.
func TestModulePathMatchesReleasedMajor(t *testing.T) {
	major := latestReleasedMajor(t)
	got := declaredModulePath(t)

	want := modulePathBase
	if major >= 2 {
		want += "/v" + strconv.Itoa(major)
	}

	if got != want {
		t.Errorf("go.mod declares module %q, want %q\n"+
			"CHANGELOG.md's newest release is v%d.x. A v2+ module path must end in /vN "+
			"or the Go proxy rejects every tag of that major.", got, want, major)
	}
}

// TestModulePathBaseUnchanged catches a rename or typo in the module path that
// still happens to carry a plausible suffix.
func TestModulePathBaseUnchanged(t *testing.T) {
	got := majorSuffixRE.ReplaceAllString(declaredModulePath(t), "")

	if got != modulePathBase {
		t.Errorf("go.mod module path (suffix stripped) = %q, want %q", got, modulePathBase)
	}
}

// declaredModulePath returns the path from go.mod's module directive.
func declaredModulePath(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	m := moduleDirectiveRE.FindSubmatch(data)
	if m == nil {
		t.Fatal("go.mod has no module directive")
	}

	return string(m[1])
}

// latestReleasedMajor returns the major version of the newest CHANGELOG entry.
// The changelog is reverse-chronological, so the first "## X.Y.Z" heading is
// the version the next tag will publish.
func latestReleasedMajor(t *testing.T) int {
	t.Helper()

	data, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}

	m := changelogHeadingRE.FindSubmatch(data)
	if m == nil {
		t.Fatal(`CHANGELOG.md has no version heading matching "## X.Y.Z"`)
	}

	major, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parse major version from %q: %v", strings.TrimSpace(string(m[0])), err)
	}

	return major
}

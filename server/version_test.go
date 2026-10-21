package server

import (
	"regexp"
	"testing"
)

// The release workflow compares VERSION with the tag; this keeps the constant
// a plain semantic version so the comparison means something.
func TestVersionIsWellFormed(t *testing.T) {
	semver := regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)
	if !semver.MatchString(VERSION) {
		t.Fatalf("VERSION %q is not a semantic version", VERSION)
	}
}

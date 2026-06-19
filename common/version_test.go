package common

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// semverish matches the loose "MAJOR.MINOR.PATCH[-suffix]" shape we use for
// dev builds. It's deliberately lenient — we only want to catch an empty or
// obviously malformed Version constant, not enforce strict SemVer.
var semverish = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

func TestVersionIsSet(t *testing.T) {
	require.NotEmpty(t, Version, "Version constant must not be empty")
	require.Regexp(t, semverish, Version, "Version must look like a semver string")
}

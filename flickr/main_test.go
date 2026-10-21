package flickr

import (
	"os"
	"testing"
)

// The developer's own credentials must not leak into the tests.
func TestMain(m *testing.M) {
	for _, k := range []string{EnvAPIKey, EnvAPISecret, EnvCredentialsFile} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

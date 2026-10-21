package flickr

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialsRoundTripWithPrivateMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conf")
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPISecret, "")
	if err := SaveCredentials(dir, Credentials{APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "credentials"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
		dirInfo, _ := os.Stat(dir)
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v, want 0700", dirInfo.Mode().Perm())
		}
	}
	c, err := LoadCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "key" || c.APISecret != "secret" {
		t.Fatalf("got %+v", c)
	}
}

func TestCredentialsFileFormatAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	body := "# comment\nexport FLICKR_API_KEY=\"fromfile\"\nFLICKR_API_SECRET='s3'\n\nIGNORED\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPISecret, "")
	c, err := LoadCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "fromfile" || c.APISecret != "s3" {
		t.Fatalf("got %+v", c)
	}
	t.Setenv(EnvAPIKey, "fromenv")
	c, _ = LoadCredentials(dir)
	if c.APIKey != "fromenv" || c.APISecret != "s3" {
		t.Fatalf("env override: got %+v", c)
	}
}

func TestMissingCredentialsFileIsNotAnError(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPISecret, "")
	c, err := LoadCredentials(t.TempDir())
	if err != nil || c.Complete() {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestLooseSecretFilesAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits on Windows")
	}
	dir := t.TempDir()
	for _, name := range []string{"credentials", "token.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadCredentials(dir); !errors.Is(err, ErrLoosePermissions) {
		t.Fatalf("credentials: err = %v, want ErrLoosePermissions", err)
	}
	if _, err := LoadToken(dir); !errors.Is(err, ErrLoosePermissions) {
		t.Fatalf("token: err = %v, want ErrLoosePermissions", err)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Token{Token: "t", TokenSecret: "ts", UserNSID: "1@N01", Username: "u", Perms: PermsWrite}
	if err := SaveToken(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

func TestParsePerms(t *testing.T) {
	for _, s := range []string{"read", "WRITE", " delete "} {
		if _, err := ParsePerms(s); err != nil {
			t.Errorf("ParsePerms(%q): %v", s, err)
		}
	}
	if _, err := ParsePerms("admin"); err == nil {
		t.Error("ParsePerms(admin) should fail")
	}
	if PermsRead >= PermsWrite || PermsWrite >= PermsDelete {
		t.Error("permission levels must be ordered")
	}
}

// An existing configuration directory others can write to would let them
// replace the credentials; saving tightens it to 0700.
func TestSavingTightensAnExistingLooseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits on Windows")
	}
	dir := filepath.Join(t.TempDir(), "conf")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(dir, Token{Token: "t", TokenSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestCredentialsFileCanLiveElsewhere(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPISecret, "")
	other := filepath.Join(t.TempDir(), "flickr.config.txt")
	if err := os.WriteFile(other, []byte("FLICKR_API_KEY=elsewhere\nFLICKR_API_SECRET=s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialsFile, other)
	c, err := LoadCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "elsewhere" {
		t.Fatalf("got %+v", c)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(other, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCredentials(t.TempDir()); !errors.Is(err, ErrLoosePermissions) {
			t.Fatalf("a loose file elsewhere must be refused too: %v", err)
		}
	}
}

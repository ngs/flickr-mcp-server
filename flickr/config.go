// Package flickr holds the Flickr API client: credentials, OAuth 1.0a and
// REST calls.
package flickr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Environment variables that override the credentials file.
const (
	EnvAPIKey    = "FLICKR_API_KEY"
	EnvAPISecret = "FLICKR_API_SECRET"
	// EnvCredentialsFile points at a credentials file somewhere other than
	// the configuration directory.
	EnvCredentialsFile = "FLICKR_MCP_CREDENTIALS"
)

const (
	credentialsFile = "credentials"
	tokenFile       = "token.json"
)

// Credentials identify the application (the App Garden key and secret).
type Credentials struct {
	APIKey    string
	APISecret string
}

// Complete reports whether both values are present.
func (c Credentials) Complete() bool { return c.APIKey != "" && c.APISecret != "" }

// Token is the OAuth access token of the authorized user.
type Token struct {
	Token       string `json:"oauth_token"`
	TokenSecret string `json:"oauth_token_secret"`
	UserNSID    string `json:"user_nsid"`
	Username    string `json:"username"`
	Perms       Perms  `json:"perms"`
}

// ConfigDir returns the directory holding the credentials and the token:
// $XDG_CONFIG_HOME/flickr-mcp-server, or ~/.config/flickr-mcp-server.
func ConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "flickr-mcp-server"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return filepath.Join(home, ".config", "flickr-mcp-server"), nil
}

// ErrLoosePermissions is returned for a secret file other users could read.
var ErrLoosePermissions = errors.New("file is readable by other users; run chmod 600 on it")

// LoadCredentials reads the credentials file in dir (KEY=VALUE lines) and lets
// FLICKR_API_KEY / FLICKR_API_SECRET override it. A missing file is not an
// error; a file with loose permissions is, so a leaked secret is noticed.
func LoadCredentials(dir string) (Credentials, error) {
	var c Credentials
	path := filepath.Join(dir, credentialsFile)
	if p := os.Getenv(EnvCredentialsFile); p != "" {
		path = p
	}
	values, err := readEnvFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	c.APIKey = values[EnvAPIKey]
	c.APISecret = values[EnvAPISecret]
	if v := os.Getenv(EnvAPIKey); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv(EnvAPISecret); v != "" {
		c.APISecret = v
	}
	return c, nil
}

// SaveCredentials writes the credentials file in dir with mode 0600.
func SaveCredentials(dir string, c Credentials) error {
	body := fmt.Sprintf("%s=%s\n%s=%s\n", EnvAPIKey, c.APIKey, EnvAPISecret, c.APISecret)
	return writeSecret(dir, credentialsFile, []byte(body))
}

// Pending is the request token of an authorization started with
// "auth --start" and finished with "auth --finish".
type Pending struct {
	Token       string `json:"oauth_token"`
	TokenSecret string `json:"oauth_token_secret"`
	Perms       Perms  `json:"perms"`
}

const pendingFile = "pending"

// SavePending writes the pending request token with mode 0600.
func SavePending(dir string, p Pending) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeSecret(dir, pendingFile, append(data, '\n'))
}

// LoadPending reads and removes the pending request token.
func LoadPending(dir string) (Pending, error) {
	var p Pending
	path := filepath.Join(dir, pendingFile)
	if err := checkPrivate(path); err != nil {
		return p, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- our own config file
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	return p, nil
}

// RemovePending deletes the pending request token.
func RemovePending(dir string) error {
	err := os.Remove(filepath.Join(dir, pendingFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// LoadToken reads token.json in dir. It returns fs.ErrNotExist (wrapped) when
// the user has not authorized yet.
func LoadToken(dir string) (Token, error) {
	var t Token
	path := filepath.Join(dir, tokenFile)
	if err := checkPrivate(path); err != nil {
		return t, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the path is our own config file
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("reading %s: %w", path, err)
	}
	return t, nil
}

// SaveToken writes token.json in dir with mode 0600.
func SaveToken(dir string, t Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(dir, tokenFile, append(data, '\n'))
}

func readEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	if err := checkPrivate(path); err != nil {
		return values, err
	}
	f, err := os.Open(path) // #nosec G304 -- the path is our own config file
	if err != nil {
		return values, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	return values, scanner.Err()
}

// checkPrivate refuses files that group or others can access. Windows has no
// such mode bits, so the check is skipped there.
func checkPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: %w", path, ErrLoosePermissions)
	}
	return nil
}

func writeSecret(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory as it is; one others can write to
	// would let them replace the files.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

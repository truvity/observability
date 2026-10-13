package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Secrets is what one request needs: the bot token, the shared secret and
// the recipients. An interface so the handler's tests need no files.
type Secrets interface {
	Load() (Loaded, error)
	Check() error
}

// Loaded is one consistent read of the mounted secret files.
type Loaded struct {
	BotToken     string
	SharedSecret string
	Recipients   map[string]int64
}

// FileSecrets reads the three files on every call. They live on a Secret
// volume (a tmpfs), so a read is cheap, and the kubelet swaps the files
// atomically when the Secret changes: a rotated token or a new recipient
// is used by the next page, with no restart and no watcher.
type FileSecrets struct {
	BotTokenFile     string
	SharedSecretFile string
	RecipientsFile   string
}

// recipientName is what may appear in the URL path: lower-case letters,
// digits, dot, dash and underscore. Narrow on purpose: it is a key, not a
// display name, and it appears in logs.
var recipientName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// Load reads and validates the three files.
func (f FileSecrets) Load() (Loaded, error) {
	var l Loaded
	var err error
	if l.BotToken, err = readSecret(f.BotTokenFile, "bot token"); err != nil {
		return l, err
	}
	if l.SharedSecret, err = readSecret(f.SharedSecretFile, "shared secret"); err != nil {
		return l, err
	}
	if len(l.SharedSecret) < 16 {
		return l, errors.New("the shared secret is shorter than 16 bytes")
	}
	raw, err := os.ReadFile(f.RecipientsFile)
	if err != nil {
		return l, fmt.Errorf("reading the recipients file: %w", err)
	}
	if l.Recipients, err = ParseRecipients(raw); err != nil {
		return l, err
	}
	return l, nil
}

// Check is Load without the result, for the readiness probe.
func (f FileSecrets) Check() error {
	_, err := f.Load()
	return err
}

func readSecret(path, what string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		// The path, never the content: an error here is logged.
		return "", fmt.Errorf("reading the %s file: %w", what, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("the %s file %s is empty", what, path)
	}
	return s, nil
}

// ParseRecipients reads a YAML (or JSON) mapping of recipient name to
// Telegram chat id:
//
//	alice: 123456789
//	bob: 987654321
//
// A person's chat id is the numeric id of their private chat with the bot
// (positive); a group's is negative. An empty mapping is refused: a relay
// that can reach nobody would accept every page and deliver none.
func ParseRecipients(raw []byte) (map[string]int64, error) {
	var m map[string]int64
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the recipients file is not a mapping of name to numeric chat id: %w", err)
	}
	if len(m) == 0 {
		return nil, errors.New("the recipients file names no recipient")
	}
	for name, id := range m {
		if !recipientName.MatchString(name) {
			return nil, fmt.Errorf("recipient name %q must match %s", name, recipientName)
		}
		if id == 0 {
			return nil, fmt.Errorf("recipient %q has chat id 0", name)
		}
	}
	return m, nil
}

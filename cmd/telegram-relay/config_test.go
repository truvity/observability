package main

import (
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestFlagsFallBackToEnvironment(t *testing.T) {
	c, err := ParseFlags(flag.NewFlagSet("t", flag.ContinueOnError),
		[]string{"--bot-token-file=/flag/token"},
		env(map[string]string{
			"TELEGRAM_RELAY_BOT_TOKEN_FILE":     "/env/token",
			"TELEGRAM_RELAY_SHARED_SECRET_FILE": "/env/secret",
			"TELEGRAM_RELAY_RECIPIENTS_FILE":    "/env/recipients.yaml",
			"TELEGRAM_RELAY_SEND_TIMEOUT":       "2s",
		}))
	require.NoError(t, err)
	assert.Equal(t, "/flag/token", c.BotTokenFile, "a flag wins over the environment")
	assert.Equal(t, "/env/secret", c.SharedSecretFile)
	assert.Equal(t, "/env/recipients.yaml", c.RecipientsFile)
	assert.Equal(t, 2*time.Second, c.SendTimeout)
	assert.Equal(t, ":8080", c.Listen)
	assert.Equal(t, ":9090", c.AdminListen)
	assert.NoError(t, c.Check())
}

func TestBadEnvironmentValueIsAnError(t *testing.T) {
	_, err := ParseFlags(flag.NewFlagSet("t", flag.ContinueOnError), nil,
		env(map[string]string{"TELEGRAM_RELAY_SEND_TIMEOUT": "soon"}))
	assert.ErrorContains(t, err, "TELEGRAM_RELAY_SEND_TIMEOUT")
}

func TestCheckRefusals(t *testing.T) {
	ok := Config{
		Listen: ":8080", AdminListen: ":9090",
		BotTokenFile: "a", SharedSecretFile: "b", RecipientsFile: "c",
		TelegramAPIURL: "https://api.telegram.org", SendTimeout: time.Second,
	}
	require.NoError(t, ok.Check())

	for name, mutate := range map[string]func(*Config){
		"no token file":      func(c *Config) { c.BotTokenFile = "" },
		"no secret file":     func(c *Config) { c.SharedSecretFile = "" },
		"no recipients file": func(c *Config) { c.RecipientsFile = "" },
		"same ports":         func(c *Config) { c.AdminListen = c.Listen },
		"relative api url":   func(c *Config) { c.TelegramAPIURL = "api.telegram.org" },
		"timeout too long":   func(c *Config) { c.SendTimeout = 3 * time.Second },
		"timeout zero":       func(c *Config) { c.SendTimeout = 0 },
	} {
		c := ok
		mutate(&c)
		assert.Error(t, c.Check(), name)
	}
}

func TestParseRecipients(t *testing.T) {
	m, err := ParseRecipients([]byte("alice: 123\nops-group: -1001234\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"alice": 123, "ops-group": -1001234}, m)

	m, err = ParseRecipients([]byte(`{"bob": 456}`))
	require.NoError(t, err)
	assert.Equal(t, int64(456), m["bob"])

	for name, raw := range map[string]string{
		"empty":         "",
		"empty map":     "{}",
		"not a number":  "alice: '@alice'",
		"zero":          "alice: 0",
		"bad name":      "Alice Smith: 1",
		"path-ish name": "../x: 1",
		"a list":        "- 1\n- 2\n",
	} {
		_, err := ParseRecipients([]byte(raw))
		assert.Error(t, err, name)
	}
}

// Every receiver's credential is a mounted file, and every file is mounted.
//
// Alertmanager's receivers here never carry a secret inline: a Slack
// bot token, a webhook URL, the deadman's URL and (since 0.10.0) a Telegram
// bot token each arrive as a Secret mounted into the VMAlertmanager pod,
// read with `*_file`. That has two ways to go wrong that a golden diff
// shows but nobody reads for: a `*_file` path under a directory no volume
// is mounted at (Alertmanager starts, and fails every delivery with "no
// such file"), and an inline credential key (`api_url`, `bot_token`, `url`)
// creeping back into the config, which puts the credential in the
// release's manifest and in git.
//
// This checks both, against every rendered VMAlertmanager in every golden.
package tests

import (
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// inlineCredentialKeys are the receiver keys that would carry the
// credential itself rather than a path to it.
var inlineCredentialKeys = map[string]bool{
	"api_url":   true, // slack_configs, an incoming webhook
	"app_token": true, // slack_configs, a Slack app's bot token
	"url":       true, // webhook_configs
	"bot_token": true, // telegram_configs
}

func TestEveryReceiverCredentialIsAMountedFile(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var files, telegram, slack, bearers int

	for _, g := range goldens {
		for _, doc := range renderedDocs(t, g) {
			if doc["kind"] != "VMAlertmanager" {
				continue
			}

			mounts := map[string]string{} // mountPath -> volume name
			if list, ok := dig(doc, "spec", "volumeMounts").([]any); ok {
				for _, m := range list {
					mm := m.(map[string]any)
					mounts[mm["mountPath"].(string)] = mm["name"].(string)
				}
			}
			secretVolumes := map[string]bool{}
			if list, ok := dig(doc, "spec", "volumes").([]any); ok {
				for _, v := range list {
					vm := v.(map[string]any)
					if dig(vm, "secret", "secretName") != nil {
						secretVolumes[vm["name"].(string)] = true
					}
				}
			}

			raw, _ := dig(doc, "spec", "configRawYaml").(string)
			require.NotEmptyf(t, raw, "%s: a VMAlertmanager with no configRawYaml", g)

			var cfg struct {
				Receivers []map[string]any `yaml:"receivers"`
			}
			require.NoErrorf(t, yaml.Unmarshal([]byte(raw), &cfg), "%s: configRawYaml is not YAML", g)

			for _, r := range cfg.Receivers {
				for kind, v := range r {
					if !strings.HasSuffix(kind, "_configs") {
						continue
					}
					for _, c := range v.([]any) {
						// A bearer for a receiver's far end is a mounted file too.
						if cred, ok := dig(c.(map[string]any), "http_config", "authorization", "credentials_file").(string); ok {
							files++
							bearers++
							vol, mounted := mounts[path.Dir(cred)]
							if assert.Truef(t, mounted,
								"%s: receiver %v reads credentials_file=%s, but no volume is mounted at %s", g, r["name"], cred, path.Dir(cred)) {
								assert.Truef(t, secretVolumes[vol],
									"%s: receiver %v reads credentials_file from volume %q, which is not a Secret", g, r["name"], vol)
							}
						}
						for key, val := range c.(map[string]any) {
							assert.Falsef(t, inlineCredentialKeys[key],
								"%s: receiver %v has an inline %q — a credential in the rendered config, and so in the release's manifest",
								g, r["name"], key)

							if !strings.HasSuffix(key, "_file") {
								continue
							}
							files++
							if kind == "telegram_configs" {
								telegram++
							}
							if kind == "slack_configs" {
								slack++
								assert.Equalf(t, "app_token_file", key,
									"%s: receiver %v reads its Slack credential from %s; only a bot token's app_token_file honours `channel`", g, r["name"], key)
							}
							dir := path.Dir(val.(string))
							vol, mounted := mounts[dir]
							if assert.Truef(t, mounted,
								"%s: receiver %v reads %s=%s, but no volume is mounted at %s — every delivery would fail with \"no such file\"",
								g, r["name"], key, val, dir) {
								assert.Truef(t, secretVolumes[vol],
									"%s: receiver %v reads %s from volume %q, which is not a Secret", g, r["name"], key, vol)
							}
						}
					}
				}
			}
		}
	}

	// A check that found nothing to check proves nothing.
	require.Positive(t, files, "no *_file receiver keys found in any golden; this test would pass vacuously")
	require.Positive(t, telegram, "no telegram_configs found in any golden; the Telegram cases are missing")
	require.Positive(t, bearers, "no http_config.authorization.credentials_file found in any golden; the watchdog-bearer case is missing")
	require.Positive(t, slack, "no slack_configs found in any golden; the Slack cases are missing")
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is everything the relay reads at start. Secrets are never values
// here, only the paths of files: the bot token, the shared secret and the
// recipients are mounted from a Secret and re-read on every request, so a
// rotation needs no restart.
type Config struct {
	Listen           string
	AdminListen      string
	BotTokenFile     string
	SharedSecretFile string
	RecipientsFile   string
	TelegramAPIURL   string
	SendTimeout      time.Duration
}

// envPrefix names the environment variable that sets a flag when the flag
// itself is not given: --bot-token-file is TELEGRAM_RELAY_BOT_TOKEN_FILE.
const envPrefix = "TELEGRAM_RELAY_"

// ParseFlags reads the configuration from args, falling back to the
// environment for every flag the arguments leave out.
func ParseFlags(fs *flag.FlagSet, args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	var c Config
	fs.StringVar(&c.Listen, "listen", ":8080", "address of the webhook endpoint (POST /v1/notify/{recipient})")
	fs.StringVar(&c.AdminListen, "admin-listen", ":9090", "address of /metrics, /healthz and /readyz")
	fs.StringVar(&c.BotTokenFile, "bot-token-file", "", "file holding the Telegram bot token (required)")
	fs.StringVar(&c.SharedSecretFile, "shared-secret-file", "", "file holding the secret every request must carry (required)")
	fs.StringVar(&c.RecipientsFile, "recipients-file", "", "YAML or JSON file mapping a recipient name to a Telegram chat id (required)")
	fs.StringVar(&c.TelegramAPIURL, "telegram-api-url", "https://api.telegram.org", "base URL of the Telegram Bot API")
	fs.DurationVar(&c.SendTimeout, "send-timeout", 2500*time.Millisecond, "budget for one Telegram call; GoAlert waits 3s for the whole webhook")
	if err := fs.Parse(args); err != nil {
		return c, err
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var errs []error
	fs.VisitAll(func(f *flag.Flag) {
		if set[f.Name] {
			return
		}
		env := envPrefix + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := lookupEnv(env); ok {
			if err := f.Value.Set(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
	})
	return c, errors.Join(errs...)
}

// Check refuses a configuration that could start and never deliver.
func (c Config) Check() error {
	var errs []error
	for name, v := range map[string]string{
		"--bot-token-file":     c.BotTokenFile,
		"--shared-secret-file": c.SharedSecretFile,
		"--recipients-file":    c.RecipientsFile,
		"--listen":             c.Listen,
		"--admin-listen":       c.AdminListen,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if c.Listen != "" && c.Listen == c.AdminListen {
		errs = append(errs, errors.New("--listen and --admin-listen must differ: /metrics must not be reachable through the webhook port"))
	}
	if u, err := url.Parse(c.TelegramAPIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("--telegram-api-url %q is not an absolute http(s) URL", c.TelegramAPIURL))
	}
	if c.SendTimeout <= 0 || c.SendTimeout >= 3*time.Second {
		errs = append(errs, fmt.Errorf("--send-timeout %s must be above 0 and below 3s, GoAlert's own webhook timeout", c.SendTimeout))
	}
	return errors.Join(errs...)
}

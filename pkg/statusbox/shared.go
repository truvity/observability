package statusbox

import (
	"errors"
	"fmt"
	"strings"
)

// This file is the small surface a second backend (pkg/statusbox/ec2) shares
// with the Lightsail one: the same checks on the instances, the same names for
// the secrets a Config may reference, and the same user-data wrapper. Nothing
// here changes what CloudInit renders.

// ValidateInstances reports every problem with the instances and the hostnames
// of the public ones, with the same messages Args.validate gives: a name that
// would need escaping, two instances on one port, an empty Config, a public
// instance with no hostname, more than one private instance.
func ValidateInstances(instances []Instance, hostnames map[string]string) error {
	_, errs := validateInstances(instances, hostnames)
	return errors.Join(errs...)
}

// AlertURLName is the environment variable an Args.Secrets.AlertURLs key
// becomes: ALERT_URL_<KEY>, upper-cased.
func AlertURLName(key string) string { return alertURLName(key) }

// ValidateSecretNames reports every problem with the keys a backend maps to
// secrets: alertKeys as Secrets.AlertURLs keys, envNames as Secrets.Env keys.
// The same rules as Args.validate: a key must be a valid name, an env name may
// not be one statusbox reserves, and no env name may equal an ALERT_URL_<KEY>
// an alert key already produces.
func ValidateSecretNames(alertKeys, envNames []string) error {
	var errs []error

	alertURLNames := map[string]string{}

	for _, k := range alertKeys {
		if !alertKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("statusbox: alert key %q is not a valid name (%s): it becomes the suffix of an environment variable, "+
				"ALERT_URL_%s, that a Config may reference", k, alertKeyRE, strings.ToUpper(k)))
		}

		alertURLNames[alertURLName(k)] = k
	}

	for _, k := range envNames {
		where := fmt.Sprintf("statusbox: env name %q", k)

		switch {
		case !envNameRE.MatchString(k):
			errs = append(errs, fmt.Errorf("%s is not a valid environment-variable name (%s)", where, envNameRE))
		case reservedEnvNames[k]:
			errs = append(errs, fmt.Errorf("%s is reserved by statusbox itself", where))
		case alertURLNames[k] != "":
			errs = append(errs, fmt.Errorf("%s collides with alert key %q, which already becomes the environment variable %s", where, alertURLNames[k], k))
		}
	}

	return errors.Join(errs...)
}

// ValidateTrustedCAs checks an Args.TrustedCAs bundle: one or more PEM
// CERTIFICATE blocks, each a CA.
func ValidateTrustedCAs(bundle string) error { return parseTrustedCAs(bundle) }

// WrapUserData gzips and base64-encodes a multi-line script into the
// `#!/bin/bash` user-data cloud-init boots from it (see wrapUserData).
func WrapUserData(script string) (string, error) { return wrapUserData(script) }

// GzipBase64 compresses and base64-encodes s so it can travel as the body of a
// quoted heredoc.
func GzipBase64(s string) (string, error) { return gzipBase64(s) }

// ShellQuote single-quotes s for interpolation into a rendered script.
func ShellQuote(s string) string { return shellQuote(s) }

// ChecksumFor finds the sha256 of the release asset called name in a
// checksums.txt body (goreleaser's `<sha256>  <filename>` lines).
func ChecksumFor(checksums, name, version string) (string, error) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[len(fields)-1] != name {
			continue
		}

		if len(fields[0]) != 64 {
			return "", fmt.Errorf("statusbox: checksums.txt for %s: %q is not a sha256 sum (want 64 hex characters, got %d)", version, fields[0], len(fields[0]))
		}

		return fields[0], nil
	}

	return "", fmt.Errorf("statusbox: checksums.txt for %s carries no entry for %s: it was not attached to that release", version, name)
}

// ReleaseURL is where a release asset of this repository is downloaded from.
func ReleaseURL(version, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", releaseRepo, version, asset)
}

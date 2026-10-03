package lambdaext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The RFC 8693 constants this uses.
const (
	grantTypeExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	typeJWT           = "urn:ietf:params:oauth:token-type:jwt"
)

// errRefused is an exchange the issuer declined, as opposed to a transport
// failure.
var errRefused = errors.New("lambdaext: the exchange was refused")

// exchanger trades a subject token for one audienced elsewhere (RFC 8693).
// It is the minimal client of the access-roster issuer's token endpoint:
// the extension needs the one exchange and nothing else, and this module
// must not import the issuer's own repository.
type exchanger struct {
	// Issuer is the token issuer's base URL.
	Issuer string
	// ClientID is presented as HTTP Basic with an empty password, which is
	// how a public client identifies itself.
	ClientID string
	// Client is the transport; nil uses one with a thirty-second timeout.
	Client *http.Client
}

type exchangedToken struct {
	AccessToken string
	// Expires is the zero time when the issuer stated no lifetime.
	Expires time.Time
}

// Exchange trades subject for a token audienced at audience.
func (e *exchanger) Exchange(ctx context.Context, subject, subjectType, audience string) (exchangedToken, error) {
	switch {
	case e == nil || strings.TrimSpace(e.Issuer) == "":
		return exchangedToken{}, errors.New("lambdaext: no issuer is configured")
	case strings.TrimSpace(subject) == "":
		return exchangedToken{}, errors.New("lambdaext: no subject token to exchange")
	case strings.TrimSpace(audience) == "":
		return exchangedToken{}, errors.New("lambdaext: no audience was asked for")
	}
	if subjectType == "" {
		subjectType = typeJWT
	}
	form := url.Values{
		"grant_type":         {grantTypeExchange},
		"subject_token":      {subject},
		"subject_token_type": {subjectType},
		"audience":           {audience},
		"scope":              {"openid"},
	}
	endpoint := strings.TrimSuffix(e.Issuer, "/") + "/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return exchangedToken{}, fmt.Errorf("lambdaext: build the exchange: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Form-encoded BEFORE Basic (RFC 6749 section 2.3.1): a client id with a
	// colon would otherwise split at the wrong place.
	request.SetBasicAuth(url.QueryEscape(e.ClientID), "")

	httpClient := e.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return exchangedToken{}, fmt.Errorf("lambdaext: exchange at %s: %w", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return exchangedToken{}, fmt.Errorf("lambdaext: read the exchange: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &failure)
		detail := strings.TrimSpace(failure.Description)
		if detail == "" {
			detail = strings.TrimSpace(failure.Error)
		}
		if detail == "" {
			detail = response.Status
		}
		return exchangedToken{}, fmt.Errorf("%w: %s", errRefused, detail)
	}
	var granted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &granted); err != nil {
		return exchangedToken{}, fmt.Errorf("lambdaext: parse the exchange: %w", err)
	}
	if granted.AccessToken == "" {
		return exchangedToken{}, errors.New("lambdaext: the exchange returned no token")
	}
	out := exchangedToken{AccessToken: granted.AccessToken}
	if granted.ExpiresIn > 0 {
		out.Expires = time.Now().Add(time.Duration(granted.ExpiresIn) * time.Second)
	}
	return out, nil
}

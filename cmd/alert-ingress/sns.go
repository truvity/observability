package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SHA-1 is SignatureVersion 1 of the envelope format itself, not a choice made here.
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Envelope is the notification wrapper every message arrives in. Every
// field the signature covers is read from here and nowhere else: a value
// pulled from the parsed body instead would be exactly the kind of thing
// the signature exists to protect, reached before it was checked.
type Envelope struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL"`
	Token            string `json:"Token"`
}

// signingHost is the shape of the ONLY host this service will ever fetch
// a certificate from, or GET a confirmation URL against. Pinned here
// rather than read from anywhere else, because both SigningCertURL and
// SubscribeURL arrive INSIDE the thing being verified: a check that
// trusted either before the signature is confirmed would let whoever sent
// the message point this service at a certificate of their own choosing
// and sign their own alerts with it.
var signingHost = regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com$`)

// signedFields is the field order the provider signs, per message Type.
// It is fixed by the envelope format, not a choice this file makes:
// changing it produces a canonical string that does not match what the
// sender actually signed, and every message fails to verify.
var signedFields = map[string][]string{
	"Notification":             {"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"},
	"SubscriptionConfirmation": {"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"},
	"UnsubscribeConfirmation":  {"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"},
}

// field returns e's own value for one signed field name, and whether that
// field is present at all. Subject is the one field that is absent rather
// than empty on an ordinary Notification, and an absent field is omitted
// from the signed string entirely rather than signed as "".
func (e Envelope) field(name string) (string, bool) {
	switch name {
	case "Message":
		return e.Message, true
	case "MessageId":
		return e.MessageID, true
	case "Subject":
		return e.Subject, e.Subject != ""
	case "SubscribeURL":
		return e.SubscribeURL, true
	case "Timestamp":
		return e.Timestamp, true
	case "Token":
		return e.Token, true
	case "TopicArn":
		return e.TopicArn, true
	case "Type":
		return e.Type, true
	default:
		return "", false
	}
}

// canonicalString builds the exact bytes the provider signed: each field
// name and value in order, each followed by its own newline.
func canonicalString(e Envelope) (string, error) {
	order, ok := signedFields[e.Type]
	if !ok {
		return "", fmt.Errorf("message Type %q is not one this service knows how to verify", e.Type)
	}

	var b strings.Builder

	for _, name := range order {
		value, present := e.field(name)
		if !present {
			continue
		}

		b.WriteString(name)
		b.WriteByte('\n')
		b.WriteString(value)
		b.WriteByte('\n')
	}

	return b.String(), nil
}

// isSigningHost is the production predicate: a host matches the
// provider's signing domain and nothing else. It is exported as a value
// (isSigningHost, not a hardcoded call inside checkURL) so a test can
// prove the PREDICATE ITSELF refuses a hostile URL without needing a
// Verifier, a certificate, or a network at all — and so that the same
// predicate, not a copy of it, is what production actually runs.
func isSigningHost(host string) bool {
	return signingHost.MatchString(host)
}

// checkURL reports whether raw is an https URL whose HOST — never a
// substring of the raw text — passes v.allowedHost. Checking the parsed
// Host rather than searching the string is what closes the trick where a
// hostile domain EMBEDS the real one earlier in the string, either after
// it (https://sns.us-east-1.amazonaws.com.attacker.example/cert.pem, a
// valid subdomain of attacker.example) or before it as a path
// (https://attacker.example/sns.us-east-1.amazonaws.com/cert.pem, a valid
// path on attacker.example). What actually receives the TCP connection is
// u.Host, and that is the only thing worth checking.
func (v *Verifier) checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL: %w", raw, err)
	}

	if u.Scheme != "https" {
		return nil, fmt.Errorf("%q does not use https", raw)
	}

	if !v.allowedHost(u.Hostname()) {
		return nil, fmt.Errorf("%q is not on the provider's signing domain", raw)
	}

	return u, nil
}

// Verifier checks a message's signature and performs subscription
// confirmation. Its HTTP client is overridden in tests so a fixture never
// causes an outbound connection.
type Verifier struct {
	HTTPClient *http.Client

	// allowedHost decides whether a host taken from INSIDE a message —
	// SigningCertURL, SubscribeURL — may be connected to. Defaulted to
	// isSigningHost, the pinned production domain; overridden only in
	// tests, so a fixture can be served from localhost without ever
	// weakening what production actually pins.
	allowedHost func(host string) bool

	// certCache holds a certificate's already-parsed public key by its
	// URL. Signing certificates are long-lived, and fetching one fresh
	// for every message would mean every request this service answers
	// costs the provider an extra GET for no reason.
	certCache map[string]*rsa.PublicKey
}

// NewVerifier returns a Verifier using client, or a five-second default
// client when client is nil, pinned to the provider's real signing
// domain.
func NewVerifier(client *http.Client) *Verifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	return &Verifier{HTTPClient: client, allowedHost: isSigningHost, certCache: map[string]*rsa.PublicKey{}}
}

// publicKey returns the RSA public key from the certificate at certURL,
// fetching it only if certURL's host passes v.allowedHost.
func (v *Verifier) publicKey(certURL string) (*rsa.PublicKey, error) {
	if key, ok := v.certCache[certURL]; ok {
		return key, nil
	}

	u, err := v.checkURL(certURL)
	if err != nil {
		return nil, fmt.Errorf("signing certificate URL refused: %w", err)
	}

	resp, err := v.HTTPClient.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("fetching signing certificate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching signing certificate: status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading signing certificate: %w", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("signing certificate at %s is not PEM-encoded", certURL)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing signing certificate: %w", err)
	}

	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("signing certificate at %s does not carry an RSA key", certURL)
	}

	v.certCache[certURL] = key

	return key, nil
}

// Verify checks e's signature against the certificate at its own
// SigningCertURL. It is not optional: a public endpoint that posts to
// Alertmanager on request is otherwise a way to page the whole
// organisation from a single curl, and this is the one gate between
// "reachable on the internet" and "believed."
func (v *Verifier) Verify(e Envelope) error {
	plaintext, err := canonicalString(e)
	if err != nil {
		return err
	}

	key, err := v.publicKey(e.SigningCertURL)
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}

	var (
		hashed []byte
		hashFn crypto.Hash
	)

	switch e.SignatureVersion {
	case "", "1":
		sum := sha1.Sum([]byte(plaintext))
		hashed, hashFn = sum[:], crypto.SHA1
	case "2":
		sum := sha256.Sum256([]byte(plaintext))
		hashed, hashFn = sum[:], crypto.SHA256
	default:
		return fmt.Errorf("SignatureVersion %q is neither 1 nor 2", e.SignatureVersion)
	}

	if err := rsa.VerifyPKCS1v15(key, hashFn, hashed, sig); err != nil {
		return fmt.Errorf("signature does not verify: %w", err)
	}

	return nil
}

// Confirm performs the one outbound request this service makes that
// changes anything in the cloud: a GET to the SubscribeURL the provider
// put inside an already-verified message. It holds no credential of its
// own to the cloud; the URL it fetches carries whatever proves the
// request is authorised, entirely on the provider's side.
func (v *Verifier) Confirm(e Envelope) error {
	u, err := v.checkURL(e.SubscribeURL)
	if err != nil {
		return fmt.Errorf("SubscribeURL refused: %w", err)
	}

	resp, err := v.HTTPClient.Get(u.String())
	if err != nil {
		return fmt.Errorf("confirming subscription: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("confirming subscription: status %d", resp.StatusCode)
	}

	return nil
}

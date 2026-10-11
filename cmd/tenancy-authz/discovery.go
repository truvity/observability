package main

import "encoding/json"

// discoveryDocument is the OIDC provider metadata vmauth fetches to learn
// where this service's keys are. Only what a verifier reads is in it.
func discoveryDocument(issuer string) []byte {
	b, err := json.Marshal(map[string]any{
		"issuer":                                issuer,
		"jwks_uri":                              issuer + "/jwks",
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
	})
	if err != nil {
		panic(err) // a map of strings
	}
	return b
}

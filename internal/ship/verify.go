// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ascTokenTTL  = 20 * time.Minute
	ascAudience  = "appstoreconnect-v1"
	ascAPIBase   = "https://api.appstoreconnect.apple.com"
	playScope    = "https://www.googleapis.com/auth/androidpublisher"
	playTokenTTL = time.Hour
)

// HTTPDo is injectable for hermetic verify tests (default: http.DefaultClient.Do).
var HTTPDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// ValidateEnvelopeShape checks local crypto shape without hitting stores.
func ValidateEnvelopeShape(env *Envelope) error {
	if env == nil {
		return nil
	}
	if env.ASC != nil && env.ASC.P8 != "" {
		if _, err := parseECDSAP256(env.ASC.P8); err != nil {
			return fmt.Errorf("asc: %w", err)
		}
		if env.ASC.KeyID == "" || env.ASC.IssuerID == "" {
			return fmt.Errorf("asc: need key_id and issuer_id with p8")
		}
	}
	if len(env.PlayServiceAccount) > 0 {
		if _, _, _, err := parsePlaySA(env.PlayServiceAccount); err != nil {
			return fmt.Errorf("play_service_account: %w", err)
		}
	}
	return nil
}

// VerifyEnvelopeLive hits ASC / Play with read-only probes (🎯T133.3).
func VerifyEnvelopeLive(env *Envelope) error {
	if err := ValidateEnvelopeShape(env); err != nil {
		return err
	}
	if env.ASC != nil && env.ASC.P8 != "" {
		tok, err := mintASCToken(env.ASC)
		if err != nil {
			return fmt.Errorf("asc token: %w", err)
		}
		if err := probeGET(ascAPIBase+"/v1/apps?limit=1", "Bearer "+tok); err != nil {
			return fmt.Errorf("asc verify: %w", err)
		}
	}
	if len(env.PlayServiceAccount) > 0 {
		if _, err := mintPlayToken(env.PlayServiceAccount); err != nil {
			return fmt.Errorf("play verify: %w", err)
		}
	}
	return nil
}

func parsePrivateKey(pemStr string) (any, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, fmt.Errorf("private key is not valid PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func parseECDSAP256(pemStr string) (*ecdsa.PrivateKey, error) {
	key, err := parsePrivateKey(pemStr)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("want EC key, got %T", key)
	}
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("want P-256, got %s", ec.Curve.Params().Name)
	}
	return ec, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mintASCToken(c *ASCCreds) (string, error) {
	ec, err := parseECDSAP256(c.P8)
	if err != nil {
		return "", err
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.KeyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": c.IssuerID,
		"iat": now.Unix(),
		"exp": now.Add(ascTokenTTL).Unix(),
		"aud": ascAudience,
	})
	signing := b64url(header) + "." + b64url(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, ec, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64url(sig), nil
}

func parsePlaySA(raw json.RawMessage) (clientEmail, tokenURI, privateKey string, err error) {
	var sa struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		TokenURI    string `json:"token_uri"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return "", "", "", err
	}
	if sa.Type != "service_account" {
		return "", "", "", fmt.Errorf("expected type service_account")
	}
	if sa.PrivateKey == "" || sa.ClientEmail == "" {
		return "", "", "", fmt.Errorf("missing client_email or private_key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return sa.ClientEmail, sa.TokenURI, sa.PrivateKey, nil
}

func mintPlayToken(raw json.RawMessage) (string, error) {
	email, tokenURI, pkPEM, err := parsePlaySA(raw)
	if err != nil {
		return "", err
	}
	key, err := parsePrivateKey(pkPEM)
	if err != nil {
		return "", err
	}
	rk, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("service-account keys are RSA; got %T", key)
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss":   email,
		"scope": playScope,
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(playTokenTTL).Unix(),
	})
	signing := b64url(header) + "." + b64url(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	assertion := signing + "." + b64url(sig)

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequest(http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := HTTPDo(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if out.AccessToken == "" {
		return "", fmt.Errorf("token exchange failed: %s %s", out.Error, out.Desc)
	}
	return out.AccessToken, nil
}

func probeGET(urlStr, auth string) error {
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	resp, err := HTTPDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

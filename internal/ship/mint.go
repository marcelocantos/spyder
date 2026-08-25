// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// MintPlayUpload generates a PKCS12 upload keystore in memory for squz
// (single alias "upload"). Minicades must import an existing multi-alias
// JKS — mint refuses that studio (🎯T133.3).
func MintPlayUpload(studio string) (*PlayUploadCreds, error) {
	studio, err := NormalizeStudio(studio)
	if err != nil {
		return nil, err
	}
	if studio == StudioMinicades {
		return nil, fmt.Errorf("play-upload mint refuses studio minicades — import the existing multi-alias JKS via secret import (do not rotate Play upload certs)")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"spyder play-upload"},
			CommonName:   "upload",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	passBytes := make([]byte, 24)
	if _, err := rand.Read(passBytes); err != nil {
		return nil, err
	}
	// Alphanumeric password — safe for Gradle property files.
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	pass := make([]byte, len(passBytes))
	for i, b := range passBytes {
		pass[i] = alphabet[int(b)%len(alphabet)]
	}
	password := string(pass)

	p12, err := pkcs12.Encode(rand.Reader, key, cert, nil, password)
	if err != nil {
		return nil, fmt.Errorf("pkcs12 encode: %w", err)
	}
	return &PlayUploadCreds{
		Keystore: p12,
		Format:   "pkcs12",
		Password: password,
		Alias:    "upload",
	}, nil
}

// SaveMintedPlayUpload merges a minted play_upload into the studio envelope.
func SaveMintedPlayUpload(studio string, creds *PlayUploadCreds) error {
	env, err := LoadStudio(studio)
	if err != nil {
		return err
	}
	if env.PlayUpload != nil && len(env.PlayUpload.Keystore) > 0 {
		return fmt.Errorf("studio %s already has play_upload — refuse overwrite (import/rotate is a separate, confirmed flow)", studio)
	}
	env.PlayUpload = creds
	return SaveStudio(studio, env)
}

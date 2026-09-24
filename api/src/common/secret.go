/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/viper"
	"golang.org/x/crypto/hkdf"
)

// Credentials that must be sent to the compute nodes (IPsec PSKs, the gateway WireGuard private key,
// WireGuard preshared keys, BGP TCP-MD5 passwords) are stored encrypted with a key derived from
// VPN_SECRET_KEY. The ciphertext carries a version prefix so a future key management change can tell
// which rows need re-encryption.
const secretVersionPrefix = "v1:"

var (
	secretKeyOnce sync.Once
	secretKey     []byte
	secretKeyErr  error
)

func loadSecretKey() ([]byte, error) {
	secretKeyOnce.Do(func() {
		raw := strings.TrimSpace(viper.GetString("vpn.secret_key"))
		if raw == "" {
			secretKeyErr = errors.New("VPN_SECRET_KEY is not configured")
			return
		}
		reader := hkdf.New(sha256.New, []byte(raw), []byte("cloudland-vpn"), []byte("vpn-secrets-v1"))
		key := make([]byte, 32)
		if _, err := io.ReadFull(reader, key); err != nil {
			secretKeyErr = err
			return
		}
		secretKey = key
	})
	return secretKey, secretKeyErr
}

// SecretStoreReady reports whether encrypted credentials can be written and read back.
func SecretStoreReady() error {
	if _, err := loadSecretKey(); err != nil {
		return NewCLError(ErrVpnSecretUnavailable, "VPN credential store is unavailable: "+err.Error(), err)
	}
	return nil
}

// EncryptSecret returns "v1:" + base64(nonce || AES-256-GCM ciphertext). An empty plaintext stays empty.
func EncryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := loadSecretKey()
	if err != nil {
		return "", NewCLError(ErrVpnSecretUnavailable, "VPN credential store is unavailable", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	return secretVersionPrefix + base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// DecryptSecret is the inverse of EncryptSecret. Values without the version prefix are rejected so a
// plaintext accidentally stored in an encrypted column is never handed to a node.
func DecryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if !strings.HasPrefix(enc, secretVersionPrefix) {
		return "", fmt.Errorf("unrecognized credential encoding")
	}
	key, err := loadSecretKey()
	if err != nil {
		return "", NewCLError(ErrVpnSecretUnavailable, "VPN credential store is unavailable", err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, secretVersionPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("credential too short")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", NewCLError(ErrVpnSecretUnavailable, "VPN credential cannot be decrypted with the configured VPN_SECRET_KEY", err)
	}
	return string(plain), nil
}

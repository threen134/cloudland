/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// selfSigned returns a PEM certificate and its PEM private key
func selfSigned(t *testing.T, cn string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

// A listener's certificate and key must come together and match (1.1.0 regression run: a broken pair froze the
// load balancer's haproxy config)
func TestValidateListenerTLS(t *testing.T) {
	cert, key := selfSigned(t, "a")
	_, otherKey := selfSigned(t, "b")
	if err := validateListenerTLS("", ""); err != nil {
		t.Errorf("plain listener refused: %v", err)
	}
	if err := validateListenerTLS(cert, key); err != nil {
		t.Errorf("matching pair refused: %v", err)
	}
	for name, c := range map[string][2]string{"cert only": {cert, ""}, "key only": {"", key}, "mismatch": {cert, otherKey}, "garbage": {"x", "y"}} {
		if err := validateListenerTLS(c[0], c[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

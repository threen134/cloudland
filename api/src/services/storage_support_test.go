/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"regexp"
	"testing"

	. "api/src/common"

	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"
)

// The key pair of a cluster: the public key in the form stc_ssh_trust.sh accepts, the private key encrypted and
// usable by ssh once decrypted, and a different pair every time
func TestStorageSSHKey(t *testing.T) {
	viper.Set("vpn.secret_key", "storage-ssh-key-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	pub, enc, err := newStorageSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/=]+$`).MatchString(pub) {
		t.Fatalf("public key %q is not what stc_ssh_trust.sh accepts", pub)
	}
	if !regexp.MustCompile(`^v1:`).MatchString(enc) {
		t.Fatalf("private key is not encrypted: %.20q", enc)
	}
	plain, err := DecryptSecret(enc)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(plain))
	if err != nil {
		t.Fatalf("decrypted private key does not parse: %v", err)
	}
	if got := string(ssh.MarshalAuthorizedKey(signer.PublicKey())); got != pub+"\n" {
		t.Fatalf("private key does not match the public key: %q vs %q", got, pub)
	}
	pub2, _, err := newStorageSSHKey()
	if err != nil || pub2 == pub {
		t.Fatalf("a second key pair must differ: %v", err)
	}
}

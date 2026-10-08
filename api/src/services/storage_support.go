/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"

	. "api/src/common"

	"golang.org/x/crypto/ssh"
)

// StorageOSRule is a system a storage kind supports. KernelPrefix, when set, is the only kernel series supported on
// it: IBM Storage Scale supports the GA generic kernel of an Ubuntu release, not the HWE ones
type StorageOSRule struct {
	ID           string `json:"id"`      // ID of /etc/os-release
	Version      string `json:"version"` // VERSION_ID of /etc/os-release
	KernelPrefix string `json:"kernel_prefix,omitempty"`
	KernelSuffix string `json:"kernel_suffix,omitempty"`
}

// storageDataDir is where the daemons of a kind keep their data and the room they need there
type storageDataDir struct {
	Path           string   `json:"path"`
	MinFreeGiB     int      `json:"min_free_gib,omitempty"`
	MinFreePercent int      `json:"min_free_percent,omitempty"`
	Roles          []string `json:"-"` // only on hosts with one of these roles; empty = every host
}

// newStorageSSHKey makes the SSH key pair of a cluster (shared-storage-design.md §6.6). The public key is the
// "ssh-ed25519 <base64>" part of an authorized_keys line (stc_ssh_trust.sh adds the restrictions and the tag), the
// private key is in OpenSSH format, encrypted for storage_clusters.ssh_priv_key
func newStorageSSHKey() (publicKey, encryptedPrivate string, err error) {
	if err = SecretStoreReady(); err != nil {
		return
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return
	}
	encryptedPrivate, err = EncryptSecret(string(pem.EncodeToMemory(block)))
	if err != nil {
		return "", "", err
	}
	publicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	return
}

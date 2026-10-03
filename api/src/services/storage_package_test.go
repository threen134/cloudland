/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// fakeGPFSInstaller builds a small file shaped like an IBM Storage Scale installer
func fakeGPFSInstaller(t *testing.T, files map[string][]byte, trailer string) []byte {
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	header := "#!/bin/sh\n# installer\nPGM_BEGIN_TGZ=6\n" +
		`PROD_FILES="Public_Keys gpfs_debs/ubuntu/ubuntu22 gpfs_debs/ubuntu/ubuntu24 gpfs_rpms/rhel9 gpfs_debs manifest"` + "\nexit 0\n"
	return append(append([]byte(header), payload.Bytes()...), trailer...)
}

func TestParseGPFSInstaller(t *testing.T) {
	files := map[string][]byte{
		"manifest": []byte("sw,rpm,gpfs.base-6.0.0-2.x86_64.rpm, Wed Feb 18 14:40:00 2026,md5sum:aaaa\n" +
			"sw,deb,gpfs.base_6.0.0-2_amd64.deb,md5sum:db11698b180a4203516d1757038ed526\n" +
			"sw,deb,gpfs.gpl_6.0.0-2_all.deb,md5sum:bbbb\n"),
		"LA_HOME/LA_en":                               utf16le("License agreement\r\nline two"),
		"LA_HOME/LI_en":                               utf16le("License information"),
		"LA_HOME/LA_zh":                               utf16le("许可协议"),
		"LA_HOME/LA_de":                               utf16le("Lizenz"),
		"gpfs_debs/gpfs.base_6.0.0-2_amd64.deb":       []byte("deb"),
		"gpfs_debs/gpfs.license.ec_6.0.0-2_amd64.deb": []byte("deb"),
		"gpfs_debs/ubuntu/ubuntu24/gpfs.librdkafka_6.0.0-2.U24.04_amd64.deb": []byte("deb"),
	}
	raw := fakeGPFSInstaller(t, files, "trailing bytes")
	meta, err := parseGPFSInstaller(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if meta.SHA256 != hex.EncodeToString(sum[:]) || meta.Size != int64(len(raw)) {
		t.Errorf("checksum %s size %d, want %x %d (trailing bytes count too)", meta.SHA256, meta.Size, sum, len(raw))
	}
	if meta.PayloadLine != 6 || meta.Version != "6.0.0.2" || meta.Edition != "erasure_code" {
		t.Errorf("payload line %d version %q edition %q", meta.PayloadLine, meta.Version, meta.Edition)
	}
	if strings.Join(meta.Distros, ",") != "ubuntu22,ubuntu24" {
		t.Errorf("distros %v", meta.Distros)
	}
	if len(meta.Manifest) != 2 || meta.Manifest["gpfs.base_6.0.0-2_amd64.deb"] != "db11698b180a4203516d1757038ed526" {
		t.Errorf("manifest %v (deb lines only)", meta.Manifest)
	}
	if meta.License["en"] != "License agreement\nline two\n\nLicense information" || meta.License["zh"] != "许可协议" || meta.License["de"] != "" {
		t.Errorf("license %q", meta.License)
	}

	broken := map[string][]byte{}
	for k, v := range files {
		broken[k] = v
	}
	delete(broken, "gpfs_debs/gpfs.base_6.0.0-2_amd64.deb")
	if _, err := parseGPFSInstaller(bytes.NewReader(fakeGPFSInstaller(t, broken, ""))); err == nil {
		t.Error("an installer without gpfs.base must be refused")
	}
	if _, err := parseGPFSInstaller(strings.NewReader("#!/bin/sh\necho hello\n")); err == nil {
		t.Error("a script without a payload must be refused")
	}
	if _, err := parseGPFSInstaller(bytes.NewReader(bytes.Repeat([]byte("x\n"), 3000))); err == nil {
		t.Error("a file without PGM_BEGIN_TGZ must be refused")
	}
}

// The real installer, when one is at hand: STORAGE_GPFS_INSTALLER=<path> (reads 1.7 GB, about a minute)
func TestParseGPFSInstallerReal(t *testing.T) {
	path := os.Getenv("STORAGE_GPFS_INSTALLER")
	if path == "" {
		t.Skip("STORAGE_GPFS_INSTALLER not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	meta, err := parseGPFSInstaller(f)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("sha256 %s size %d payload line %d version %s edition %s distros %v manifest %d debs, license languages %d, en %d chars\n",
		meta.SHA256, meta.Size, meta.PayloadLine, meta.Version, meta.Edition, meta.Distros, len(meta.Manifest), len(meta.License), len(meta.License["en"]))
	if meta.Manifest["gpfs.base_6.0.0-2_amd64.deb"] != "db11698b180a4203516d1757038ed526" {
		t.Errorf("md5 of gpfs.base %q", meta.Manifest["gpfs.base_6.0.0-2_amd64.deb"])
	}
}

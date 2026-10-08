/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestClassifyImageHeader(t *testing.T) {
	qcow2 := make([]byte, 512)
	binary.BigEndian.PutUint32(qcow2[0:4], 0x514649fb)
	binary.BigEndian.PutUint64(qcow2[24:32], 10<<30)

	mbr := make([]byte, 512)
	mbr[510], mbr[511] = 0x55, 0xAA

	html := []byte("<html><head><title>302 Found</title></head><body>moved</body></html>")
	gz := append([]byte{0x1f, 0x8b, 0x08}, make([]byte, 509)...)
	vmdk := append([]byte("KDMV"), make([]byte, 508)...)

	cases := []struct {
		name        string
		header      []byte
		size        int64
		wantFormat  string
		wantVirtual uint64
		wantErr     string
	}{
		{name: "qcow2", header: qcow2, size: 1 << 20, wantFormat: "qcow2", wantVirtual: 10 << 30},
		{name: "raw with boot signature", header: mbr, size: 2 << 30, wantFormat: "raw", wantVirtual: 2 << 30},
		{name: "html error page", header: html, size: int64(len(html)), wantErr: "boot signature"},
		{name: "raw without boot signature", header: make([]byte, 512), size: 1 << 20, wantErr: "boot signature"},
		{name: "gzip", header: gz, size: 1 << 20, wantErr: "gzip"},
		{name: "vmdk", header: vmdk, size: 1 << 20, wantErr: "vmdk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := classifyImageHeader(c.header, c.size)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got info=%+v err=%v", c.wantErr, info, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Format != c.wantFormat || info.VirtualSize != c.wantVirtual {
				t.Fatalf("got %+v, want format=%s virtual=%d", info, c.wantFormat, c.wantVirtual)
			}
		})
	}
}

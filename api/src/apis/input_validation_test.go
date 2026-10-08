/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import "testing"

func TestValidateBackendEndpoint(t *testing.T) {
	for _, ok := range []string{"192.168.62.2:8080", "10.0.0.1:1", "10.0.0.1:65535"} {
		if err := validateBackendEndpoint(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"192.168.62.2", "192.168.62.2:0", "192.168.62.2:70000", "web:80", "[::1]:80",
		"1.2.3.4:80$(reboot)", "1.2.3.4:80\n    server x 6.6.6.6:80", "1.2.3.4 :80"} {
		if err := validateBackendEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateGuestCredentials(t *testing.T) {
	if err := validateGuestCredentials("Administrator", "P@ss w0rd!'\"$`"); err != nil {
		t.Errorf("valid credentials rejected: %v", err)
	}
	for _, c := range [][2]string{{"root;id", "password1"}, {"a b", "password1"}, {"-root", "password1"}, {"root", "pass\nword1"}, {"root", "pässword1"}} {
		if err := validateGuestCredentials(c[0], c[1]); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
}

func TestValidateDownloadURL(t *testing.T) {
	if err := validateDownloadURL("https://example.com/img.qcow2?x=1&y=2"); err != nil {
		t.Errorf("valid url rejected: %v", err)
	}
	for _, bad := range []string{"http://x/a -o /root/.ssh/authorized_keys http://x/b", "http://x/a\tb", "http://x/a\nb"} {
		if err := validateDownloadURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

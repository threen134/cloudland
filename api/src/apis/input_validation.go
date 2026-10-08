/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Values checked here end up in scripts and config files on compute nodes (run as root); besides shell
// quoting (common.ShellEscape) they must not carry whitespace or control characters that split
// arguments or inject config lines.

// validateBackendEndpoint requires an IPv4:port address: it is written into the haproxy config as is
func validateBackendEndpoint(endpoint string) error {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint must be IP:port, %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("endpoint host %q is not an IPv4 address", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("endpoint port %q must be between 1 and 65535", portStr)
	}
	return nil
}

var guestUserNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// validateGuestCredentials checks the user name and password set through the guest agent
func validateGuestCredentials(userName, password string) error {
	if !guestUserNamePattern.MatchString(userName) {
		return fmt.Errorf("user name may only contain letters, digits, '.', '_' and '-'")
	}
	for _, r := range password {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return fmt.Errorf("password may only contain printable ASCII characters")
		}
	}
	return nil
}

// validateDownloadURL rejects whitespace and control characters, which the http_url validator accepts
// but would split the URL into extra curl arguments on the node
func validateDownloadURL(url string) error {
	if strings.IndexFunc(url, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("download_url must not contain whitespace or control characters")
	}
	return nil
}

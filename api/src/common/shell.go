/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"fmt"
	"strings"
)

// ShellEscape formats v for use inside a single-quoted shell word ('...').
// Commands sent to compute nodes run as root through bash -c, so every formatted value placed between
// single quotes must go through it: a quote in the value would otherwise end the word and let the rest
// run as shell code. Inside single quotes nothing else ($, `, \) is special.
func ShellEscape(v interface{}) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		s = fmt.Sprint(v)
	}
	return strings.ReplaceAll(s, "'", `'\''`)
}

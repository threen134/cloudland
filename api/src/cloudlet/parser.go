/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Parser for |:-COMMAND-:| callback markers in script stdout.
Ported from: handler.cpp frontHandler lines 80-94
*/

package cloudlet

import "strings"

const commandMarker = "|:-COMMAND-:|"

// ParseCallbackLine checks if a line contains the |:-COMMAND-:| marker
// and extracts the command after it. Returns the command and true if found.
//
// C++ handler.cpp skips strlen("|:-COMMAND:-|") + 1 characters. Both spellings are
// 13 characters, so it drops the marker plus one space; TrimSpace is equivalent for
// "|:-COMMAND-:| cmd" lines.
func ParseCallbackLine(line string) (command string, isCallback bool) {
	idx := strings.Index(line, commandMarker)
	if idx == -1 {
		return "", false
	}
	command = line[idx+len(commandMarker):]
	command = strings.TrimSpace(command)
	return command, true
}

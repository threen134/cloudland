/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import "testing"

func TestVrrpDispatchArgs(t *testing.T) {
	// What cland hands back with error=resource: the command as clapi dispatched it
	_, args := DecodeCommand("/opt/cloudland/scripts/backend/set_vrrp_ip.sh '12' '34' '4098' '52:54:00:aa:bb:cc' '192.168.196.3/24' '52:54:00:dd:ee:ff' '192.168.196.2/24' 'BACKUP' 'true'")
	id, role, ok := vrrpDispatchArgs(args)
	if !ok || id != 34 || role != "BACKUP" {
		t.Fatalf("dispatched command: id=%d role=%q ok=%v (args %v)", id, role, ok, args)
	}
	for name, command := range map[string]string{
		// A node's callback has other arguments: vrrp ID, hostid, role, mac
		"node callback": "set_vrrp_ip.sh '34' '2' 'MASTER' '52:54:00:aa:bb:cc'",
		"unknown role":  "/opt/cloudland/scripts/backend/set_vrrp_ip.sh '12' '34' '4098' 'm' 'i' 'pm' 'pi' 'PEER' 'true'",
		"bad vrrp id":   "/opt/cloudland/scripts/backend/set_vrrp_ip.sh '12' 'x' '4098' 'm' 'i' 'pm' 'pi' 'MASTER' 'true'",
		"too short":     "/opt/cloudland/scripts/backend/set_vrrp_ip.sh '12' '34'",
		"zero vrrp id":  "/opt/cloudland/scripts/backend/set_vrrp_ip.sh '12' '0' '4098' 'm' 'i' 'pm' 'pi' 'MASTER' 'true'",
	} {
		_, args := DecodeCommand(command)
		if _, _, ok := vrrpDispatchArgs(args); ok {
			t.Errorf("%s: accepted %v", name, args)
		}
	}
}

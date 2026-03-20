/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	rlog "api/src/utils/log"
)

var logger = rlog.MustGetLogger("services")

// Init initializes the services package, creating system admin user and org
func Init() {
	AdminInit()
	RebuildAlarmRulesOnStartup()
}

/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import "os"

var (
	Version = "unknown"
	sysInfoAdminInstance = &SysInfoAdmin{}
)

type SysInfoAdmin struct{}

const (
	VersionFile = "/opt/cloudland/version"
)

func (v *SysInfoAdmin) GetVersion() (ver string) {
	logger.Info("ENTER GetVersion: get system version")
	defer func() {
		logger.Infof("EXIT GetVersion: version=%s", ver)
	}()
	if Version == "unknown" {
		version, err := os.ReadFile(VersionFile)
		if err != nil {
			logger.Warningf("failed to read version file: %v", err)
		} else {
			Version = string(version)
		}
	}
	return Version
}

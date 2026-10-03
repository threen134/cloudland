/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// GPFS (IBM Storage Scale) as a storage backend: shared-storage-design.md §7

import (
	"encoding/json"
	"strings"

	"api/src/model"
)

func init() {
	registerStorageBackend(gpfsBackend{})
}

type gpfsBackend struct{}

// gpfsParams are the parameters of a GPFS cluster (§7.2)
type gpfsParams struct {
	storageCommonParams
	FsName       string `json:"fs_name,omitempty"`
	BlockSize    string `json:"block_size,omitempty"`
	DataReplicas int    `json:"data_replicas,omitempty"`
	PagepoolMiB  int    `json:"pagepool_mib,omitempty"`
}

func (gpfsBackend) Kind() string { return model.StorageKindGPFS }

func (gpfsBackend) Roles() []string {
	return []string{model.StorageRoleAdmin, model.StorageRoleQuorum, model.StorageRoleNSD, model.StorageRoleClient}
}

func (gpfsBackend) DiskRole() string { return model.StorageRoleNSD }

func (gpfsBackend) DefaultRoles() (base, once []string) {
	return []string{model.StorageRoleQuorum, model.StorageRoleNSD}, []string{model.StorageRoleAdmin}
}

// GPFS 6.0 does not build its kernel module for the 7.0 kernel of Ubuntu 26.04, so 26.04 is not listed; IBM supports
// the GA generic kernel of a release, not the HWE ones (§2.2)
var gpfsRequirements = &StorageRequirements{
	Systems: []StorageOSRule{
		{ID: "ubuntu", Version: "22.04", KernelPrefix: "5.15.0-", KernelSuffix: "-generic"},
		{ID: "ubuntu", Version: "24.04", KernelPrefix: "6.8.0-", KernelSuffix: "-generic"},
	},
	PeerPorts:    []int{22, 1191},
	DataDirs:     []storageDataDir{{Path: "/var/mmfs", MinFreeGiB: 10}},
	KernelModule: true,
	Package:      true,
}

func (gpfsBackend) Requirements() *StorageRequirements { return gpfsRequirements }

func (b gpfsBackend) ParseParams(raw json.RawMessage) (interface{}, error) {
	p := &gpfsParams{}
	if err := decodeStorageParams(b.Kind(), raw, p); err != nil {
		return nil, err
	}
	// What gpfs_fs.sh accepts (the file system name, and the mount point /gpfs/<name>): refused here rather than at
	// the create_fs step, after everything before it ran. Empty is the default name
	if p.FsName != "" && !gpfsFsNameRe.MatchString(p.FsName) {
		return nil, planError("The file system name of a GPFS cluster starts with a letter and has only letters, digits and _, at most 32")
	}
	if p.BlockSize != "" && !strings.Contains(" 1M 2M 4M 8M 16M ", " "+p.BlockSize+" ") {
		return nil, planError("The block size of a GPFS cluster must be 1M, 2M, 4M, 8M or 16M")
	}
	if err := storageParamRange(b.Kind(), "data_replicas", p.DataReplicas, 1, 3); err != nil {
		return nil, err
	}
	if err := storageParamRange(b.Kind(), "pagepool_mib", p.PagepoolMiB, 256, 65536); err != nil {
		return nil, err
	}
	return p, nil
}

func (gpfsBackend) CheckLayout(nodes []*StorageNodePlan, disks []*StorageDiskPlan, params interface{}) error {
	p := params.(*gpfsParams)
	quorum, admin, groups := 0, 0, 0
	disksOn := map[int32]int{}
	for _, d := range disks {
		disksOn[d.Hostid]++
	}
	for _, node := range nodes {
		if hasRole(node.Roles, model.StorageRoleQuorum) {
			quorum++
		}
		if hasRole(node.Roles, model.StorageRoleAdmin) {
			admin++
			if !hasRole(node.Roles, model.StorageRoleQuorum) {
				return planError("An admin host must also be a quorum host")
			}
		}
		if hasRole(node.Roles, model.StorageRoleNSD) {
			if disksOn[node.Hostid] == 0 {
				return planError("An NSD host needs at least one disk")
			}
			groups++
		}
	}
	if quorum%2 == 0 || (quorum == 1 && !p.Test) {
		return planError("A GPFS cluster needs an odd number of quorum hosts, at least 3 (1 only in the test layout); %d given", quorum)
	}
	if admin < 1 || admin > 2 {
		return planError("A GPFS cluster needs 1 or 2 admin hosts; %d given", admin)
	}
	// The file system descriptor quorum is counted in failure groups: two groups lose the file system with either
	// of them (§6.3)
	if groups < 3 && !p.Test {
		return planError("A GPFS cluster needs disks on at least 3 hosts (failure groups); %d given", groups)
	}
	if groups < 1 {
		return planError("A GPFS cluster needs at least one NSD host")
	}
	replicas := p.DataReplicas
	if replicas == 0 {
		replicas = 2
		if p.Test {
			replicas = 1
		}
	}
	if replicas > groups || (replicas < 2 && !p.Test) {
		return planError("Data replicas must be 2 or 3 and not more than the failure groups (%d)", groups)
	}
	return nil
}

// A host belongs to one GPFS cluster at most (§4.1)
func (gpfsBackend) HostConflict(roles, otherRoles []string) string {
	return "is in another GPFS cluster already"
}

// pagepool + 1 GiB on every member, whatever its roles (§6.7)
func (gpfsBackend) ReserveMB(roles []string, disks int, params interface{}) int32 {
	pagepool := params.(*gpfsParams).PagepoolMiB
	if pagepool <= 0 {
		pagepool = 1024
	}
	return int32(pagepool + 1024)
}

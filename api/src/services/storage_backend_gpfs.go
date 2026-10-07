/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// GPFS (IBM Storage Scale) as a storage backend: shared-storage-design.md §7

import (
	"encoding/json"
	"fmt"
	"strings"

	. "api/src/common"
	"api/src/model"
)

func init() {
	registerStorageBackend(gpfsBackend{})
}

type gpfsBackend struct{}

// gpfsParams are the parameters of a GPFS cluster (§7.2, §7.9)
type gpfsParams struct {
	storageCommonParams
	FsName       string `json:"fs_name,omitempty"`
	BlockSize    string `json:"block_size,omitempty"`
	DataReplicas int    `json:"data_replicas,omitempty"`
	// The pagepool of every member in the replica layout; of the recovery group servers in the erasure code layout
	// (the other members keep the default), at least 8 GiB there: mmvdisk refuses less
	PagepoolMiB int `json:"pagepool_mib,omitempty"`
	// replica (default) or ece: the disks of the NSD hosts go to a recovery group and the file system lives on a
	// vdisk set with an erasure code (§7.9)
	Layout string `json:"layout,omitempty"`
	// The code of the vdisk set (4+2p by default) and how much of the declustered array it takes (80% by default)
	ECECode    string `json:"ece_code,omitempty"`
	ECESetSize int    `json:"ece_set_size,omitempty"`
	// Virtual machines and emulated disks have no slot map: the slot check of the recovery group is turned off.
	// Only for tests, IBM does not support it
	NoSlotMap bool `json:"no_slot_map,omitempty"`
}

const (
	gpfsECEEdition         = "erasure_code"
	gpfsECEDefaultCode     = "4+2p"
	gpfsECEDefaultSetSize  = 80
	gpfsECEMinPagepoolMiB  = 8192
	gpfsECEMinServers      = 3
	gpfsECEMaxServers      = 32
	gpfsECEMinDisks        = 12
	gpfsECEServerExtraMiB  = 2048
	gpfsDefaultPagepoolMiB = 1024
)

// gpfsECECodes are the codes of a vdisk set, with how many pdisks a strip spans and the block sizes it takes (what
// mmvdisk vdiskset define accepts, among the block sizes of a GPFS cluster)
var gpfsECECodes = map[string]struct {
	Width  int
	Blocks string
}{
	"3WayReplication": {3, " 1M 2M "},
	"4WayReplication": {4, " 1M 2M "},
	"4+2p":            {6, " 1M 2M 4M 8M "},
	"4+3p":            {7, " 1M 2M 4M 8M "},
	"8+2p":            {10, " 1M 2M 4M 8M 16M "},
	"8+3p":            {11, " 1M 2M 4M 8M 16M "},
}

func (p *gpfsParams) ece() bool { return p.Layout == model.StorageLayoutECE }

// LayoutOf: the layout the parameters ask for
func (gpfsBackend) LayoutOf(params interface{}) string {
	if p, ok := params.(*gpfsParams); ok && p.ece() {
		return model.StorageLayoutECE
	}
	return model.StorageLayoutReplica
}

// CheckPackage: the erasure code layout runs on the erasure code edition only (gpfs.gnr is in no other installer)
func (gpfsBackend) CheckPackage(params interface{}, pkg *model.StoragePackage) error {
	if p, ok := params.(*gpfsParams); ok && p.ece() && pkg.Edition != gpfsECEEdition {
		edition := pkg.Edition
		if edition == "" {
			edition = "unknown"
		}
		return NewCLError(ErrStoragePackageState, fmt.Sprintf("The erasure code layout needs an erasure code edition package, this one is the %s edition", edition), nil)
	}
	return nil
}

// LayoutInfo: the erasure code layer of a cluster in the ece layout (the code, whether the slot check is off, the
// recovery group, vdisk set and node class once deployed); nothing for the replica layout
func (gpfsBackend) LayoutInfo(cluster *model.StorageCluster) map[string]interface{} {
	if cluster.Layout != model.StorageLayoutECE {
		return nil
	}
	p := gpfsParamsOf(cluster)
	info := map[string]interface{}{"code": p.ECECode, "no_slot_map": p.NoSlotMap}
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(cluster.Attrs), &attrs)
	for _, k := range []string{"recovery_group", "vdisk_set", "node_class"} {
		if v, ok := attrs[k].(string); ok && v != "" {
			info[k] = v
		}
	}
	return info
}

// LayoutCapabilities: the first version of the erasure code layout deploys, deletes, makes pools and renews the key;
// changing its hosts or disks (mmvdisk recoverygroup add / pdisk replace) and upgrading it come later (§7.9)
func (gpfsBackend) LayoutCapabilities(layout string) *StorageCapabilities {
	if layout != model.StorageLayoutECE {
		return nil
	}
	return &StorageCapabilities{Managed: true, Filesystems: true, Pools: true, RotateKeys: true}
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
	switch p.Layout {
	case "", model.StorageLayoutReplica:
		p.Layout = ""
		if p.ECECode != "" || p.ECESetSize != 0 || p.NoSlotMap {
			return nil, planError("ece_code, ece_set_size and no_slot_map belong to the erasure code layout (layout ece)")
		}
	case model.StorageLayoutECE:
		if p.Test {
			return nil, planError("The erasure code layout needs at least %d servers: it has no single host test layout", gpfsECEMinServers)
		}
		// The code protects the data: the file system keeps one copy
		if p.DataReplicas != 0 {
			return nil, planError("The erasure code layout takes no data_replicas: the code of the vdisk set protects the data")
		}
		if p.ECECode == "" {
			p.ECECode = gpfsECEDefaultCode
		}
		code, ok := gpfsECECodes[p.ECECode]
		if !ok {
			return nil, planError("The code of a GPFS vdisk set must be 4+2p, 4+3p, 8+2p, 8+3p, 3WayReplication or 4WayReplication")
		}
		if p.BlockSize == "" {
			p.BlockSize = "4M"
			if code.Width <= 4 {
				p.BlockSize = "2M"
			}
		}
		if !strings.Contains(code.Blocks, " "+p.BlockSize+" ") {
			return nil, planError("Code %s takes the block sizes%s", p.ECECode, strings.TrimRight(code.Blocks, " "))
		}
		if p.ECESetSize == 0 {
			p.ECESetSize = gpfsECEDefaultSetSize
		}
		if err := storageParamRange(b.Kind(), "ece_set_size", p.ECESetSize, 10, 100); err != nil {
			return nil, err
		}
		if p.PagepoolMiB == 0 {
			p.PagepoolMiB = gpfsECEMinPagepoolMiB
		}
		if p.PagepoolMiB < gpfsECEMinPagepoolMiB {
			return nil, planError("The recovery group servers need a pagepool of at least %d MiB (mmvdisk refuses less)", gpfsECEMinPagepoolMiB)
		}
	default:
		return nil, planError("The layout of a GPFS cluster is replica or ece")
	}
	return p, nil
}

// checkECELayout: the NSD hosts are the servers of one recovery group (3-32 of them, the same number of disks on
// each, all disks of one media), with at least 12 disks in all (the declustered array) and at least 2 more than the
// width of the code: mmvdisk wants the width in pdisks that are not spare space, and keeps 2 disks of spare space
// (§7.9)
func checkECELayout(nodes []*StorageNodePlan, disks []*StorageDiskPlan, p *gpfsParams) error {
	disksOn := map[int32]int{}
	media := map[string]bool{}
	for _, d := range disks {
		disksOn[d.Hostid]++
		media[d.Media] = true
	}
	servers, per := 0, -1
	for _, node := range nodes {
		if !hasRole(node.Roles, model.StorageRoleNSD) {
			if disksOn[node.Hostid] > 0 {
				return planError("Only the NSD hosts (the recovery group servers) give disks")
			}
			continue
		}
		servers++
		if per >= 0 && disksOn[node.Hostid] != per {
			return planError("Every recovery group server needs the same number of disks")
		}
		per = disksOn[node.Hostid]
	}
	if servers < gpfsECEMinServers || servers > gpfsECEMaxServers {
		return planError("A recovery group needs %d-%d servers (NSD hosts); %d given", gpfsECEMinServers, gpfsECEMaxServers, servers)
	}
	// One declustered array: SSD / NVMe beside HDDs (two arrays) comes later
	if len(media) > 1 {
		return planError("The disks of a recovery group must all be of one media for now")
	}
	total := per * servers
	if total < gpfsECEMinDisks {
		return planError("A recovery group needs at least %d disks in all; %d given", gpfsECEMinDisks, total)
	}
	if w := gpfsECECodes[p.ECECode].Width; total < w+2 {
		return planError("Code %s spans %d disks, so the recovery group needs at least %d disks (2 of spare space); %d given",
			p.ECECode, w, w+2, total)
	}
	return nil
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
	if p.ece() {
		return checkECELayout(nodes, disks, p)
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

// pagepool + 1 GiB on every member, whatever its roles (§6.7). In the erasure code layout the servers take their
// pagepool and 2 GiB more (the recovery group: log groups, vdisk map), the other members the default pagepool
func (gpfsBackend) ReserveMB(roles []string, disks int, params interface{}) int32 {
	p := params.(*gpfsParams)
	pagepool := p.PagepoolMiB
	if pagepool <= 0 {
		pagepool = gpfsDefaultPagepoolMiB
	}
	if p.ece() {
		if hasRole(roles, model.StorageRoleNSD) {
			return int32(pagepool + gpfsECEServerExtraMiB)
		}
		return int32(gpfsDefaultPagepoolMiB + 1024)
	}
	return int32(pagepool + 1024)
}

// MetricQueries are the curves of a GPFS cluster (shared-storage-design.md §14.3, appendix E), from what every member
// writes for node_exporter (backend_metrics in scripts/kvm/storage/backends/gpfs.sh): the capacity of each file system
// (every member that mounts it sees the same), the I/O of all members added up, and how many hosts run GPFS and mount
// each file system
func (gpfsBackend) MetricQueries(cluster *model.StorageCluster, window int64) []StorageMetricQuery {
	c := "cluster=" + promLabel(cluster.UUID)
	rate := func(metric string) string {
		return fmt.Sprintf(`sum(rate(%s{%s}[%ds]))`, metric, c, window)
	}
	return []StorageMetricQuery{
		{Chart: StorageChartCapacity, Series: "used", Split: "fs",
			Query: fmt.Sprintf(`max by (fs) (cloudland_gpfs_filesystem_total_bytes{%s} - cloudland_gpfs_filesystem_free_bytes{%s})`, c, c)},
		{Chart: StorageChartCapacity, Series: "total", Split: "fs", Query: fmt.Sprintf(`max by (fs) (cloudland_gpfs_filesystem_total_bytes{%s})`, c)},
		{Chart: StorageChartThroughput, Series: "read", Query: rate("cloudland_gpfs_read_bytes_total")},
		{Chart: StorageChartThroughput, Series: "write", Query: rate("cloudland_gpfs_write_bytes_total")},
		{Chart: StorageChartIOPS, Series: "read", Query: rate("cloudland_gpfs_reads_total")},
		{Chart: StorageChartIOPS, Series: "write", Query: rate("cloudland_gpfs_writes_total")},
		{Chart: StorageChartNodes, Series: "active", Query: fmt.Sprintf(`sum(cloudland_gpfs_node_active{%s})`, c)},
		{Chart: StorageChartNodes, Series: "mounted", Split: "fs", Query: fmt.Sprintf(`sum by (fs) (cloudland_gpfs_filesystem_mounted{%s})`, c)},
	}
}

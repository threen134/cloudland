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
	// replica (default); ece: the disks of the NSD hosts go to a recovery group and the file system lives on a vdisk
	// set with an erasure code (§7.9); san: the NSDs are shared LUNs, each served by the members that see it (§7.10)
	Layout string `json:"layout,omitempty"`
	// The code of the vdisk set (4+2p by default) and how much of the declustered array it takes (80% by default)
	ECECode    string `json:"ece_code,omitempty"`
	ECESetSize int    `json:"ece_set_size,omitempty"`
	// Virtual machines and emulated disks have no slot map: the slot check of the recovery group is turned off.
	// Only for tests, IBM does not support it
	NoSlotMap bool `json:"no_slot_map,omitempty"`
	// Real servers: the slot map is made by ecedrivemapping in lmr (SAS disks behind a LSI controller) or nvme mode,
	// for the user slots given (it prompts for them otherwise); without a mode the slot map must be on the servers
	SlotMode  string `json:"slot_mode,omitempty"`
	SlotRange []int  `json:"slot_range,omitempty"`
	// Mixed media (a declustered array of solid state disks beside the HDDs): the metadata vdisk set goes on the solid
	// state array with its own code (3WayReplication by default), block size (1M) and share of the array (80%); the
	// data vdisk set on the HDDs takes ece_code, block_size and ece_set_size
	ECEMetaCode      string `json:"ece_meta_code,omitempty"`
	ECEMetaBlockSize string `json:"ece_meta_block_size,omitempty"`
	ECEMetaSetSize   int    `json:"ece_meta_set_size,omitempty"`
	// What IBM supports only (16 cores, 25 Gbit/s, bare metal servers) fails the precheck instead of warning
	ECEStrict bool `json:"ece_strict,omitempty"`
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
	gpfsECEMetaCode        = "3WayReplication"
	gpfsECEMetaBlockSize   = "1M"
	// The readiness of a recovery group server (§2.3): what IBM supports, and how far apart the memory of the servers
	// may be (mmvdisk refuses more than 10%)
	gpfsECESupportCores    = 16
	gpfsECESupportLinkMbps = 25000
	gpfsECEMemSpreadPct    = 10
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
	if p, ok := params.(*gpfsParams); ok {
		if p.ece() {
			return model.StorageLayoutECE
		}
		if p.san() {
			return model.StorageLayoutSAN
		}
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
	if p.SlotMode != "" {
		info["slot_mode"] = p.SlotMode
	}
	// The metadata set only when the cluster made one (mixed media): the wizard always sends its parameters, a cluster of
	// one media has no metadata set to show
	sets := gpfsECESetsOf(cluster)
	for _, s := range sets {
		if s == gpfsECEMetaSet(cluster) {
			code, bs, _ := gpfsECEMeta(p)
			info["meta_code"], info["meta_block_size"] = code, bs
		}
	}
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(cluster.Attrs), &attrs)
	for _, k := range []string{"recovery_group", "vdisk_set", "node_class"} {
		if v, ok := attrs[k].(string); ok && v != "" {
			info[k] = v
		}
	}
	// Every vdisk set: the data sets (one more for each resize) and the metadata set of a mixed media cluster
	if len(sets) > 1 {
		info["vdisk_set"] = strings.Join(sets, ", ")
	}
	return info
}

// LayoutCapabilities: the erasure code layout takes hosts (servers into the recovery group, mmvdisk recoverygroup add;
// clients plainly) and lets them go (recoverygroup delete -N), gets disks on every server at once (recoverygroup
// resize and a vdisk set for them), replaces a failed pdisk, changes the roles that are not the server one, upgrades
// with its servers suspended one at a time (§7.9). A single disk does not leave a recovery group (only with its server
// or replaced), GNR balances its stripes itself, and a second file system would need its own vdisk set
func (gpfsBackend) LayoutCapabilities(layout string) *StorageCapabilities {
	if layout == model.StorageLayoutSAN {
		// A LUN is not replaced (the array protects it) and a second file system would need failure groups of its own
		return &StorageCapabilities{Managed: true, Filesystems: true, Pools: true, RotateKeys: true, AddNodes: true, RemoveNode: true,
			AddDisks: true, RemoveDisk: true, Rebalance: true, ChangeRoles: true, Upgrade: true, Finalize: true, RemoteMount: true}
	}
	if layout != model.StorageLayoutECE {
		return nil
	}
	return &StorageCapabilities{Managed: true, Filesystems: true, Pools: true, RotateKeys: true, AddNodes: true, RemoveNode: true,
		AddDisks: true, ReplaceDisk: true, ChangeRoles: true, Upgrade: true, Finalize: true, RemoteMount: true}
}

// gpfsECEMeta: the code, block size and share of the array of the metadata vdisk set of a mixed media cluster
func gpfsECEMeta(p *gpfsParams) (code, blockSize string, setSize int) {
	code, blockSize, setSize = p.ECEMetaCode, p.ECEMetaBlockSize, p.ECEMetaSetSize
	if code == "" {
		code = gpfsECEMetaCode
	}
	if blockSize == "" {
		blockSize = gpfsECEMetaBlockSize
	}
	if setSize == 0 {
		setSize = gpfsECEDefaultSetSize
	}
	return code, blockSize, setSize
}

// gpfsECEMedia is the media class of a disk in a recovery group: hdd, ssd or nvme (an unknown media counts as hdd)
func gpfsECEMedia(media string) string {
	switch strings.ToLower(media) {
	case "ssd", "nvme":
		return strings.ToLower(media)
	}
	return "hdd"
}

// gpfsECEArrays are the declustered arrays the disks of a recovery group make: the media of the data array, and of
// the metadata array when there are two (the solid state disks beside the HDDs); an error for media mmvdisk does not
// pair
func gpfsECEArrays(media []string) (data, meta string, err error) {
	classes := map[string]bool{}
	for _, m := range media {
		classes[gpfsECEMedia(m)] = true
	}
	switch {
	case len(classes) == 1:
		for c := range classes {
			data = c
		}
	case len(classes) == 2 && classes["hdd"]:
		data = "hdd"
		for c := range classes {
			if c != "hdd" {
				meta = c
			}
		}
	case len(classes) > 1:
		return "", "", planError("A recovery group takes disks of one media, or solid state disks of one kind (SSD or NVMe) beside HDDs")
	}
	return data, meta, nil
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
		if p.ECECode != "" || p.ECESetSize != 0 || p.NoSlotMap || p.SlotMode != "" || len(p.SlotRange) > 0 || p.ECEMetaCode != "" ||
			p.ECEMetaBlockSize != "" || p.ECEMetaSetSize != 0 || p.ECEStrict {
			return nil, planError("ece_*, no_slot_map, slot_mode and slot_range belong to the erasure code layout (layout ece)")
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
		if err := checkECEMetaParams(b.Kind(), p); err != nil {
			return nil, err
		}
		if err := checkECESlotParams(p); err != nil {
			return nil, err
		}
	case model.StorageLayoutSAN:
		if p.ECECode != "" || p.ECESetSize != 0 || p.NoSlotMap || p.SlotMode != "" || len(p.SlotRange) > 0 || p.ECEMetaCode != "" ||
			p.ECEMetaBlockSize != "" || p.ECEMetaSetSize != 0 || p.ECEStrict {
			return nil, planError("ece_*, no_slot_map, slot_mode and slot_range belong to the erasure code layout (layout ece)")
		}
		// The array protects the data: one copy
		if p.DataReplicas > 1 {
			return nil, planError("The shared disk layout keeps one copy of the data: the storage array protects it")
		}
	default:
		return nil, planError("The layout of a GPFS cluster is replica, ece or san")
	}
	return p, nil
}

// ReadinessInput: a recovery group server of the erasure code layout is checked for what mmvdisk enforces (the
// memory its pagepool takes: mmvdisk sets it to 80% of the memory at most) and what IBM supports (16 cores, 25 Gbit/s,
// bare metal), the latter only warned of unless the cluster is strict (§2.3)
func (gpfsBackend) ReadinessInput(roles []string, params interface{}) map[string]interface{} {
	p, ok := params.(*gpfsParams)
	if !ok || !p.ece() || !hasRole(roles, model.StorageRoleNSD) {
		return nil
	}
	pagepool := p.PagepoolMiB
	if pagepool < gpfsECEMinPagepoolMiB {
		pagepool = gpfsECEMinPagepoolMiB
	}
	return map[string]interface{}{"ece": true, "min_mem_mib": pagepool * 5 / 4, "min_cores": gpfsECESupportCores,
		"min_link_mbps": gpfsECESupportLinkMbps, "bare_metal": true, "strict": p.ECEStrict}
}

// gpfsECEMemorySpread: the memory of the recovery group servers may differ by 10% at most (mmvdisk refuses a server
// configuration otherwise). mem is the memory of each server in MiB, as their prechecks reported it
func gpfsECEMemorySpread(mem map[int32]int64) error {
	var lo, hi int64
	for _, m := range mem {
		if m <= 0 {
			continue
		}
		if lo == 0 || m < lo {
			lo = m
		}
		if m > hi {
			hi = m
		}
	}
	if lo > 0 && (hi-lo)*100 > lo*gpfsECEMemSpreadPct {
		return fmt.Errorf("the memory of the recovery group servers differs by more than %d%% (%d to %d MiB): mmvdisk configures only servers alike",
			gpfsECEMemSpreadPct, lo, hi)
	}
	return nil
}

// checkECEMetaParams: the code and block size of the metadata vdisk set (used when the servers have solid state disks
// beside their HDDs)
func checkECEMetaParams(kind string, p *gpfsParams) error {
	if p.ECEMetaCode != "" {
		if _, ok := gpfsECECodes[p.ECEMetaCode]; !ok {
			return planError("The metadata code of a GPFS vdisk set must be 4+2p, 4+3p, 8+2p, 8+3p, 3WayReplication or 4WayReplication")
		}
	}
	code, bs, _ := gpfsECEMeta(p)
	if !strings.Contains(gpfsECECodes[code].Blocks, " "+bs+" ") {
		return planError("Metadata code %s takes the block sizes%s", code, strings.TrimRight(gpfsECECodes[code].Blocks, " "))
	}
	return storageParamRange(kind, "ece_meta_set_size", p.ECEMetaSetSize, 10, 100)
}

// checkECESlotParams: a slot mode takes the slot range ecedrivemapping maps (it prompts without one); emulated disks
// have no slots to map
func checkECESlotParams(p *gpfsParams) error {
	switch p.SlotMode {
	case "":
		if len(p.SlotRange) > 0 {
			return planError("slot_range goes with slot_mode")
		}
		return nil
	case "lmr", "nvme":
	default:
		return planError("The slot mode is lmr (SAS disks behind a LSI controller) or nvme")
	}
	if p.NoSlotMap {
		return planError("A cluster without slot map has no slot mode")
	}
	if len(p.SlotRange) != 2 || p.SlotRange[0] < 0 || p.SlotRange[1] < p.SlotRange[0] || p.SlotRange[1] > 9999 {
		return planError("slot_range is the first and last user slot, [min, max]")
	}
	return nil
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
	mediaList := []string{}
	for m := range media {
		mediaList = append(mediaList, m)
	}
	data, meta, err := gpfsECEArrays(mediaList)
	if err != nil {
		return err
	}
	// Every server has the same disks of each media: the topologies of the servers must match
	byMedia := map[string]map[int32]int{}
	for _, d := range disks {
		c := gpfsECEMedia(d.Media)
		if byMedia[c] == nil {
			byMedia[c] = map[int32]int{}
		}
		byMedia[c][d.Hostid]++
	}
	totals := map[string]int{}
	for c, on := range byMedia {
		count := -1
		for _, node := range nodes {
			if !hasRole(node.Roles, model.StorageRoleNSD) {
				continue
			}
			if count >= 0 && on[node.Hostid] != count {
				return planError("Every recovery group server needs the same number of %s disks", strings.ToUpper(c))
			}
			count = on[node.Hostid]
			totals[c] += on[node.Hostid]
		}
	}
	total := totals[data]
	if total < gpfsECEMinDisks {
		return planError("A recovery group needs at least %d disks in its data array; %d given", gpfsECEMinDisks, total)
	}
	if w := gpfsECECodes[p.ECECode].Width; total < w+2 {
		return planError("Code %s spans %d disks, so the recovery group needs at least %d disks (2 of spare space); %d given",
			p.ECECode, w, w+2, total)
	}
	if meta != "" {
		code, _, _ := gpfsECEMeta(p)
		if w := gpfsECECodes[code].Width; totals[meta] < w+2 {
			return planError("The metadata code %s spans %d disks, so the %s array needs at least %d disks; %d given",
				code, w, strings.ToUpper(meta), w+2, totals[meta])
		}
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
	if p.san() {
		return checkSANLayout(nodes, disks)
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
		// Every member exports the same numbers of the GPFS storage pools and the filesets: the largest of them
		{Chart: StorageChartGpfsPools, Series: "used", Split: "fspool", Query: fmt.Sprintf(
			`label_join(max by (fs, gpfs_pool) (cloudland_gpfs_pool_total_bytes{%s} - cloudland_gpfs_pool_free_bytes{%s}), "fspool", "/", "fs", "gpfs_pool")`, c, c)},
		{Chart: StorageChartGpfsPools, Series: "total", Split: "fspool", Query: fmt.Sprintf(
			`label_join(max by (fs, gpfs_pool) (cloudland_gpfs_pool_total_bytes{%s}), "fspool", "/", "fs", "gpfs_pool")`, c)},
		{Chart: StorageChartInodes, Series: "pool", Split: "pool", Query: fmt.Sprintf(
			`max by (pool) (cloudland_gpfs_fileset_inodes_used{%s}) / max by (pool) (cloudland_gpfs_fileset_inodes_max{%s} > 0) * 100`, c, c)},
	}
}

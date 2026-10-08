/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The shared disk layout of GPFS (shared-storage-design.md §7.10): the NSDs are LUNs of a SAN (FC, iSCSI, multipath)
// that several members see. A LUN is claimed on every host that is to serve it (a record per host, the same stable id
// on each); it becomes one NSD whose server list is those hosts, the first of them rotated over the LUNs. The array
// protects the data, so the file system keeps one copy of data and metadata and every LUN is in failure group 1.
// Servers come and go with mmchnsd (online since GPFS 5.0); a LUN leaves with its last server or removed as a whole.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"api/src/model"

	"gorm.io/gorm"
)

const (
	// mmcrnsd takes at most 8 servers for an NSD
	gpfsSANMaxServers = 8
	gpfsSANGroup      = 1
)

func (p *gpfsParams) san() bool { return p.Layout == model.StorageLayoutSAN }

// TakesSharedDisks: the shared disk layout claims shared LUNs (and only those); the other layouts refuse them
func (gpfsBackend) TakesSharedDisks(params interface{}) bool {
	p, ok := params.(*gpfsParams)
	return ok && p.san()
}

// DiskSiblings are the records of the same LUN on the other hosts that serve it (the shared disk layout); none for a
// disk of one host
func (gpfsBackend) DiskSiblings(cluster *model.StorageCluster, disk *model.StorageClusterDisk, disks []*model.StorageClusterDisk) []*model.StorageClusterDisk {
	if cluster.Layout != model.StorageLayoutSAN {
		return nil
	}
	out := []*model.StorageClusterDisk{}
	for _, d := range disks {
		if d.DiskID == disk.DiskID && d.ID != disk.ID {
			out = append(out, d)
		}
	}
	return out
}

// checkSANLayout: every LUN is served by 1-8 hosts, every NSD host serves a LUN
func checkSANLayout(nodes []*StorageNodePlan, disks []*StorageDiskPlan) error {
	hostsOf := map[string]map[int32]bool{}
	disksOn := map[int32]int{}
	for _, d := range disks {
		if hostsOf[d.DiskID] == nil {
			hostsOf[d.DiskID] = map[int32]bool{}
		}
		hostsOf[d.DiskID][d.Hostid] = true
		disksOn[d.Hostid]++
	}
	if len(hostsOf) == 0 {
		return planError("The shared disk layout needs at least one shared LUN")
	}
	for id, hosts := range hostsOf {
		if len(hosts) > gpfsSANMaxServers {
			return planError("LUN %s is served by %d hosts: an NSD takes at most %d servers", id, len(hosts), gpfsSANMaxServers)
		}
	}
	for _, n := range nodes {
		if hasRole(n.Roles, model.StorageRoleNSD) && disksOn[n.Hostid] == 0 {
			return planError("An NSD host of the shared disk layout serves at least one LUN")
		}
	}
	return nil
}

// gpfsSANNames names the NSDs of a shared disk cluster: cl<cluster>s<n>, one name for a LUN on every host that serves
// it; a LUN keeps the name it has
func gpfsSANNames(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) map[int64]string {
	sorted := append([]*model.StorageClusterDisk{}, disks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	byLUN := map[string]string{}
	used := 0
	for _, d := range sorted {
		if d.Name == "" {
			continue
		}
		byLUN[d.DiskID] = d.Name
		var c, n int
		if _, err := fmt.Sscanf(d.Name, "cl%ds%d", &c, &n); err == nil && n > used {
			used = n
		}
	}
	for _, d := range sorted {
		if _, ok := byLUN[d.DiskID]; !ok {
			used++
			byLUN[d.DiskID] = fmt.Sprintf("cl%ds%d", cluster.ID, used)
		}
	}
	names := map[int64]string{}
	for _, d := range disks {
		names[d.ID] = byLUN[d.DiskID]
	}
	return names
}

// gpfsSANLUN is a LUN of a shared disk cluster: its records, one per host that serves it or leaves
type gpfsSANLUN struct {
	id      string
	records []*model.StorageClusterDisk
}

// hosts are the hosts serving the LUN once the task is done (not those leaving), by hostid
func (l *gpfsSANLUN) hosts() []int32 {
	out := []int32{}
	for _, d := range l.records {
		if d.Status != model.StorageDiskRemoving {
			out = append(out, d.Hostid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (l *gpfsSANLUN) allStatus(status string) bool {
	for _, d := range l.records {
		if d.Status != status {
			return false
		}
	}
	return len(l.records) > 0
}

func (l *gpfsSANLUN) anyStatus(status string) bool {
	for _, d := range l.records {
		if d.Status == status {
			return true
		}
	}
	return false
}

// gpfsSANLUNs groups the records of a cluster by LUN, in the order of their names
func gpfsSANLUNs(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) ([]*gpfsSANLUN, map[int64]string) {
	names := gpfsSANNames(cluster, disks)
	byID := map[string]*gpfsSANLUN{}
	luns := []*gpfsSANLUN{}
	for _, d := range disks {
		l := byID[d.DiskID]
		if l == nil {
			l = &gpfsSANLUN{id: d.DiskID}
			byID[d.DiskID] = l
			luns = append(luns, l)
		}
		l.records = append(l.records, d)
	}
	sort.Slice(luns, func(i, j int) bool {
		return gpfsSANIndex(names[luns[i].records[0].ID]) < gpfsSANIndex(names[luns[j].records[0].ID])
	})
	return luns, names
}

func gpfsSANIndex(name string) int {
	var c, n int
	_, _ = fmt.Sscanf(name, "cl%ds%d", &c, &n)
	return n
}

// gpfsSANServers is the server list of a LUN: its hosts, the first rotated by the index of the LUN so the LUNs are
// spread over their servers
func gpfsSANServers(hosts []int32, index int) []int32 {
	if len(hosts) == 0 {
		return hosts
	}
	k := index % len(hosts)
	return append(append([]int32{}, hosts[k:]...), hosts[:k]...)
}

// gpfsSANPrimary: whether a record is the one of its LUN that does what is done once for the LUN (wipe it): the
// lowest host among those given
func gpfsSANPrimary(d *model.StorageClusterDisk, siblings []*model.StorageClusterDisk, hosts map[int32]bool) bool {
	for _, s := range siblings {
		if s.DiskID == d.DiskID && hosts[s.Hostid] && s.Hostid < d.Hostid {
			return false
		}
	}
	return true
}

// gpfsSANChecksEmpty: whether the record of a shared LUN is the one that checks the LUN is empty, and wipes it when
// asked, while it is resolved: only a LUN new to the cluster (every record of it claiming) is, by the lowest of its
// hosts. A LUN the cluster uses already holds the file system's data: a host that comes to serve it only checks its
// identity, whatever its hostid (the lowest host among all of them would otherwise wipe a live NSD)
func gpfsSANChecksEmpty(d *model.StorageClusterDisk, disks []*model.StorageClusterDisk) bool {
	claiming := map[int32]bool{}
	for _, s := range disks {
		if s.DiskID != d.DiskID {
			continue
		}
		if s.Status != model.StorageDiskClaiming {
			return false
		}
		claiming[s.Hostid] = true
	}
	return gpfsSANPrimary(d, disks, claiming)
}

// gpfsSANWipedHere: whether the record of a shared LUN is the one its wipe comes from, when the hosts given leave: no
// other host keeps serving the LUN, and this is the lowest of those leaving
func gpfsSANWipedHere(d *model.StorageClusterDisk, disks []*model.StorageClusterDisk, leaving map[int32]bool) bool {
	for _, s := range disks {
		if s.DiskID == d.DiskID && s.ID != d.ID && !leaving[s.Hostid] {
			return false
		}
	}
	return gpfsSANPrimary(d, disks, leaving)
}

// gpfsSANNSDInput: one NSD for each new LUN (all its records new), on the device the first of its servers resolved
func gpfsSANNSDInput(db *gorm.DB, cluster *model.StorageCluster, disks []*model.StorageClusterDisk, devices map[string]string,
	ips map[int32]string) (interface{}, error) {
	luns, names := gpfsSANLUNs(cluster, disks)
	nsds := []map[string]interface{}{}
	for _, l := range luns {
		if !l.allStatus(model.StorageDiskClaiming) {
			continue
		}
		name := names[l.records[0].ID]
		servers := gpfsSANServers(l.hosts(), gpfsSANIndex(name))
		dev := devices[storageDiskKey(servers[0], l.id)]
		if dev == "" {
			return nil, fmt.Errorf("LUN %s was not resolved on %s", l.id, hostName(db, servers[0]))
		}
		addrs := []string{}
		for _, h := range servers {
			addrs = append(addrs, ips[h])
		}
		nsds = append(nsds, map[string]interface{}{"name": name, "device": dev, "server": strings.Join(addrs, ","),
			"failure_group": gpfsSANGroup, "usage": diskAttr(l.records[0], "usage"), "pool": diskAttr(l.records[0], "gpfs_pool")})
	}
	return map[string]interface{}{"action": "create", "cluster_uuid": cluster.UUID, "nsds": nsds}, nil
}

// gpfsSANStanzas are the stanzas of the new LUNs for mmcrfs / mmadddisk: one per LUN, in failure group 1. A LUN
// gaining a server is in the file system already
func gpfsSANStanzas(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) []map[string]interface{} {
	luns, names := gpfsSANLUNs(cluster, disks)
	out := []map[string]interface{}{}
	for _, l := range luns {
		if !l.allStatus(model.StorageDiskClaiming) {
			continue
		}
		d := l.records[0]
		out = append(out, map[string]interface{}{"name": names[d.ID], "usage": diskAttr(d, "usage"), "failure_group": gpfsSANGroup,
			"pool": diskAttr(d, "gpfs_pool")})
	}
	return out
}

// gpfsSANLeaving are the NSDs of LUNs that leave as a whole (every record of them removing): dropped from the file
// system and deleted. A LUN that keeps a server only loses the ones leaving (san_servers)
func gpfsSANLeaving(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) []string {
	luns, names := gpfsSANLUNs(cluster, disks)
	out := []string{}
	for _, l := range luns {
		if l.allStatus(model.StorageDiskRemoving) {
			out = append(out, names[l.records[0].ID])
		}
	}
	return out
}

// gpfsSANServersInput: the LUNs that keep their NSD but gain or lose servers get their new server list (mmchnsd)
func gpfsSANServersInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, err := storageHostIPs(db, nodeHostids(nodes, ""))
	if err != nil {
		return nil, err
	}
	luns, names := gpfsSANLUNs(cluster, disks)
	nsds := []map[string]interface{}{}
	for _, l := range luns {
		changing := l.anyStatus(model.StorageDiskClaiming) || l.anyStatus(model.StorageDiskRemoving)
		if !changing || l.allStatus(model.StorageDiskClaiming) || l.allStatus(model.StorageDiskRemoving) {
			continue
		}
		name := names[l.records[0].ID]
		addrs := []string{}
		for _, h := range gpfsSANServers(l.hosts(), gpfsSANIndex(name)) {
			addrs = append(addrs, ips[h])
		}
		nsds = append(nsds, map[string]interface{}{"name": name, "servers": strings.Join(addrs, ",")})
	}
	return map[string]interface{}{"action": "servers", "cluster_uuid": cluster.UUID, "nsds": nsds}, nil
}

// gpfsSANChangeTaskPlan: the changes of a shared disk cluster. New LUNs are made NSDs and added; LUNs gaining or losing
// a server get their server list; a host leaves after the LUNs only it served (their data moves first)
func gpfsSANChangeTaskPlan(task string, cluster *model.StorageCluster, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	nodes := scope.Nodes
	admins := gpfsWorkingAdmins(nodes)
	if len(admins) == 0 {
		return nil, planError("No admin host of the cluster is left to run the change")
	}
	step := func(name, s string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: s, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	diskHosts := gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)
	luns, _ := gpfsSANLUNs(cluster, scope.Disks)
	newLUNs := 0
	for _, l := range luns {
		if l.allStatus(model.StorageDiskClaiming) {
			newLUNs++
		}
	}
	disksIn := func() []*StorageStepPlan {
		if len(diskHosts) == 0 {
			return nil
		}
		// Every host serving a LUN resolves it: the nsddevices exit of each lists its device
		plan := []*StorageStepPlan{step("resolve_disks", n, diskHosts, 5*time.Minute)}
		if newLUNs > 0 {
			plan = append(plan, step("create_nsd", a, admins, 15*time.Minute))
		}
		plan = append(plan, step("san_servers", a, admins, 15*time.Minute))
		if newLUNs > 0 {
			plan = append(plan, step("add_disks", a, admins, 60*time.Minute))
		}
		return plan
	}
	switch task {
	case StorageTaskAddNodes:
		newHosts := gpfsHostsBy(nodes, model.StorageNodeJoining, "")
		if len(newHosts) == 0 {
			return nil, planError("No host joins the cluster")
		}
		plan := []*StorageStepPlan{
			step("precheck", n, newHosts, 10*time.Minute),
			step("join", n, newHosts, 10*time.Minute),
			step("fetch_package", n, newHosts, 30*time.Minute),
			step("install", n, newHosts, 30*time.Minute),
			step("build_gpl", n, newHosts, 15*time.Minute),
			step("ssh_trust", n, gpfsHostsBy(nodes, "", ""), 5*time.Minute),
			step("add_node", a, admins, 30*time.Minute),
		}
		plan = append(plan, disksIn()...)
		return append(plan, step("finish", n, newHosts, 5*time.Minute)), nil
	case StorageTaskAddDisks:
		if len(diskHosts) == 0 {
			return nil, planError("No disk joins the cluster")
		}
		return disksIn(), nil
	case StorageTaskRemoveDisk:
		hosts := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(hosts) == 0 {
			return nil, planError("No disk leaves the cluster")
		}
		return []*StorageStepPlan{
			step("remove_disks", a, admins, 24*time.Hour),
			step("release_disks", n, hosts, 30*time.Minute),
		}, nil
	case StorageTaskRemoveNode:
		leaving := gpfsHostsBy(nodes, model.StorageNodeLeaving, "")
		if len(leaving) != 1 {
			return nil, planError("One host leaves at a time")
		}
		plan := []*StorageStepPlan{}
		if len(gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)) > 0 {
			// The LUNs it shares lose it as a server first, then the LUNs only it served leave with their data moved
			plan = append(plan, step("san_servers", a, admins, 15*time.Minute))
			if len(gpfsSANLeaving(cluster, scope.Disks)) > 0 {
				plan = append(plan, step("remove_disks", a, admins, 24*time.Hour))
			}
		}
		plan = append(plan, step("remove_node", a, admins, 30*time.Minute))
		if !scope.Offline {
			plan = append(plan, step("leave", n, leaving, 30*time.Minute))
		}
		return plan, nil
	case StorageTaskReplaceDisk:
		return nil, planError("A LUN of the shared disk layout is not replaced: the array protects its data; add a LUN and remove this one")
	}
	return gpfsChangeTaskPlan(task, scope)
}

/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The health watchdog and the storage alarms against PostgreSQL with the fake cland (shared-storage-design.md §14):
// the round sends the check to an admin host, a report from a host outside the cluster is refused, a report updates
// the cluster, its hosts, disks and file systems, the alarms fire after the delay (at once for nearfull and the pool
// usage), change severity and resolve, the file system alarm skips offline hosts, a stale report turns the health
// unknown, and the deletion of a cluster resolves what still fires. The node scripts are covered by the WSL tests.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/src/model"
)

func TestStorageHealthPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	cluster := f.cluster
	stamp := time.Now().UnixNano() % 1000000
	disks := []*model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&disks).Error)
	for i, d := range disks {
		must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Update("name", fmt.Sprintf("hd%d", i+1)).Error)
	}
	pool, _ := f.createPool(fmt.Sprintf("hp-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	t.Cleanup(func() {
		db.Unscoped().Where("vm_uuid = ?", cluster.UUID).Delete(&model.AlarmEvent{})
		for _, hostid := range h {
			f.setHostStatus(hostid, 1)
		}
	})
	names := []string{}
	for _, hostid := range h {
		names = append(names, hostName(db, hostid))
	}
	report := func(health string, nodeStates, diskStates []string, flags ...string) *StorageHealthReport {
		r := &StorageHealthReport{Health: health, Summary: "test", Flags: flags,
			Filesystems: []StorageHealthFs{{Name: "fs1", Mounted: true, Total: 6000 * gib, Free: 5900 * gib}}}
		for i, s := range nodeStates {
			r.Nodes = append(r.Nodes, StorageHealthItem{Name: strings.ToUpper(names[i]) + ".cloudland.local", State: s})
		}
		for i, s := range diskStates {
			r.Disks = append(r.Disks, StorageHealthItem{Name: fmt.Sprintf("hd%d", i+1), State: s})
		}
		if health != "healthy" {
			r.Messages = []string{"something is wrong"}
		}
		return r
	}
	load := func() (*model.StorageCluster, *StorageHealthInfo) {
		t.Helper()
		c := &model.StorageCluster{}
		must(t, db.Take(c, cluster.ID).Error)
		return c, ParseStorageHealthInfo(c.HealthInfo)
	}
	// backdate moves the first sight of every condition 3 minutes back, as if the delay went by
	backdate := func() {
		t.Helper()
		_, info := load()
		for k, v := range info.Since {
			info.Since[k] = v.Add(-3 * time.Minute)
		}
		raw, _ := json.Marshal(info)
		must(t, db.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Update("health_info", string(raw)).Error)
	}
	// events waits for the alarm events of the cluster (they are written in the background) and counts them by status
	events := func(firing, resolved int) {
		t.Helper()
		var nf, nr int64
		for i := 0; i < 50; i++ {
			db.Model(&model.AlarmEvent{}).Where("vm_uuid = ? AND status = ?", cluster.UUID, "firing").Count(&nf)
			db.Model(&model.AlarmEvent{}).Where("vm_uuid = ? AND status = ?", cluster.UUID, "resolved").Count(&nr)
			if int(nf) == firing && int(nr) == resolved {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("alarm events: %d firing, %d resolved; want %d, %d", nf, nr, firing, resolved)
	}
	firingKeys := func() string {
		_, info := load()
		keys := []string{}
		for k, a := range info.Firing {
			keys = append(keys, k+"="+a.Severity)
		}
		return strings.Join(sortedStrings(keys), ",")
	}
	nodes := func() []*model.StorageClusterNode {
		ns := []*model.StorageClusterNode{}
		must(t, db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&ns).Error)
		return ns
	}

	// The round sends the check to the admin host, with the file systems to look at
	f.cland.take()
	c, _ := load()
	maintainStorageHealth(ctx, c, nodes())
	sent := ""
	for _, cmd := range f.cland.take() {
		if strings.Contains(cmd, "stc_health.sh '"+cluster.UUID+"'") {
			sent = cmd
		}
	}
	if !strings.HasPrefix(sent, fmt.Sprintf("inter=%d ", h[0])) || !strings.Contains(sent, `"filesystems":[{"mount":"/gpfs/fs1","name":"fs1"}]`) ||
		!strings.Contains(sent, `"kind":"gpfs"`) {
		t.Fatalf("health check sent: %q", sent)
	}
	if c, _ := load(); c.Health != model.StorageHealthUnknown {
		t.Fatalf("health without a report: %s", c.Health)
	}

	// A host outside the cluster does not report on it
	if err := HandleStorageHealth(ctx, stcHosts[3], cluster.UUID, report("healthy", nil, nil)); err == nil {
		t.Fatal("a report from a host outside the cluster was taken")
	}

	// A healthy report: the hosts (named in capitals, with a domain), disks and the file system are updated
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, report("healthy", []string{"active", "active", "active"}, []string{"up", "up", "up"})))
	c, info := load()
	if c.Health != model.StorageHealthHealthy || c.HealthAt == nil || info.Hostid != h[0] || info.Summary != "test" {
		t.Fatalf("after a healthy report: %s %v %+v", c.Health, c.HealthAt, info)
	}
	for _, n := range nodes() {
		if n.State != "active" || n.CheckedAt == nil {
			t.Fatalf("host %d: state %q checked %v", n.Hostid, n.State, n.CheckedAt)
		}
	}
	d := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ? AND name = ?", cluster.ID, "hd2").Take(d).Error)
	if d.State != "up" || d.CheckedAt == nil {
		t.Fatalf("disk: %+v", d)
	}
	fs := &model.StorageFilesystem{}
	must(t, db.Where("cluster_id = ?", cluster.ID).Take(fs).Error)
	if fs.CapacityBytes != 6000*gib || fs.FreeBytes != 5900*gib || fs.CapacityAt == nil {
		t.Fatalf("file system capacity: %+v", fs)
	}

	// A host and a disk down, the cluster in warning: nothing fires before the delay
	bad := report("warning", []string{"active", "down", "active"}, []string{"up", "down", "up"})
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, bad))
	if _, info := load(); len(info.Firing) != 0 || len(info.Since) != 3 {
		t.Fatalf("before the delay: since %v, firing %v", info.Since, info.Firing)
	}
	// Past the delay: the three alarms fire, owned by the system organization
	backdate()
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, bad))
	want := fmt.Sprintf("cluster=warning,disk:hd2=warning,node:%d=warning", h[1])
	if got := firingKeys(); got != want {
		t.Fatalf("firing %s, want %s", got, want)
	}
	events(3, 0)
	ev := &model.AlarmEvent{}
	must(t, db.Where("vm_uuid = ? AND alert_name = ?", cluster.UUID, StorageAlarmNodeDown).Take(ev).Error)
	if ev.Owner != strconv.FormatInt(platformOrgID(ctx), 10) || ev.Severity != "warning" || !strings.Contains(ev.Summary, "is down") {
		t.Fatalf("node alarm event: %+v", ev)
	}
	// The cluster turns error: the cluster alarm is raised again as critical; nearfull fires at once
	worse := report("error", []string{"active", "down", "active"}, []string{"up", "down", "up"}, "nearfull")
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, worse))
	want = fmt.Sprintf("cluster=critical,disk:hd2=warning,flag:nearfull=critical,node:%d=warning", h[1])
	if got := firingKeys(); got != want {
		t.Fatalf("firing %s, want %s", got, want)
	}
	events(4, 1)
	// A host that can not check changes nothing of the alarms
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, &StorageHealthReport{Health: "unknown", Error: "mmgetstate gave no answer"}))
	if c, info := load(); c.Health != model.StorageHealthUnknown || info.Error == "" || len(info.Firing) != 4 {
		t.Fatalf("after an unknown report: %s %+v", c.Health, info)
	}
	// Healthy again: everything resolves
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, report("healthy", []string{"active", "active", "active"}, []string{"up", "up", "up"})))
	if got := firingKeys(); got != "" {
		t.Fatalf("still firing: %s", got)
	}
	events(0, 5)

	// A report that leaves a disk out (its query timed out) keeps that disk's alarm: missing is not recovered
	active := []string{"active", "active", "active"}
	diskDown := report("warning", active, []string{"up", "down", "up"})
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, diskDown))
	backdate()
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, diskDown))
	events(2, 5)
	partial := report("warning", active, nil)
	partial.Disks = []StorageHealthItem{{Name: "hd1", State: "up"}, {Name: "hd3", State: "up"}}
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, partial))
	if got := firingKeys(); got != "cluster=warning,disk:hd2=warning" {
		t.Fatalf("a disk left out of the report: %s", got)
	}
	events(2, 5)
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, report("healthy", active, []string{"up", "up", "up"})))
	events(0, 7)

	// Nobody can check it (the monitors do not answer): the alarm of an unreachable cluster after the delay
	unk := &StorageHealthReport{Health: "unknown", Error: "the monitors of the cluster do not answer"}
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, unk))
	if got := firingKeys(); got != "" {
		t.Fatalf("before the delay: %s", got)
	}
	backdate()
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, unk))
	if got := firingKeys(); got != "reach=critical" {
		t.Fatalf("unreachable: %s", got)
	}
	events(1, 7)
	ev = &model.AlarmEvent{}
	must(t, db.Where("vm_uuid = ? AND status = ?", cluster.UUID, "firing").Take(ev).Error)
	if ev.AlertName != StorageAlarmClusterUnhealthy || !strings.Contains(ev.Summary, "can not be checked: the monitors") {
		t.Fatalf("unreachable event: %+v", ev)
	}
	must(t, HandleStorageHealth(ctx, h[0], cluster.UUID, report("healthy", active, []string{"up", "up", "up"})))
	events(0, 8)

	// The usage of a pool: warning at once at 85%, critical at 95% (the warning resolves), resolved at 10%
	setUsage := func(pct int64) {
		must(t, db.Model(&model.StoragePool{}).Where("id = ?", pool.ID).Updates(map[string]interface{}{"capacity_bytes": 100 * gib, "used_bytes": pct * gib}).Error)
	}
	round := func() {
		c, _ := load()
		maintainStorageHealth(ctx, c, nodes())
	}
	setUsage(85)
	round()
	if got := firingKeys(); got != "pool:"+pool.UUID+"=warning" {
		t.Fatalf("85%%: %s", got)
	}
	setUsage(95)
	round()
	if got := firingKeys(); got != "pool:"+pool.UUID+"=critical" {
		t.Fatalf("95%%: %s", got)
	}
	setUsage(10)
	round()
	if got := firingKeys(); got != "" {
		t.Fatalf("10%%: %s", got)
	}
	events(0, 10)

	// A member that does not reach the pool (its file system is not mounted): after the delay; not for an offline host
	must(t, db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", h[2], pool.ID).
		Updates(map[string]interface{}{"status": model.HyperPoolUnavailable, "reason": "/gpfs/fs1 is not mounted"}).Error)
	round()
	if got := firingKeys(); got != "" {
		t.Fatalf("before the delay: %s", got)
	}
	backdate()
	round()
	if got := firingKeys(); got != fmt.Sprintf("mount:%d:%s=critical", h[2], pool.UUID) {
		t.Fatalf("not mounted: %s", got)
	}
	events(1, 10)
	f.setHostStatus(h[2], 10)
	round()
	if got := firingKeys(); got != "" {
		t.Fatalf("offline host: %s", got)
	}
	events(0, 11)
	f.setHostStatus(h[2], 1)

	// A report gone stale: unknown health
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Update("health_at", time.Now().Add(-10*time.Minute)).Error)
	round()
	if c, _ := load(); c.Health != model.StorageHealthUnknown {
		t.Fatalf("stale report: %s", c.Health)
	}

	// The deletion of a cluster resolves what still fires: the file system alarm, and the unreachable cluster (its
	// report went stale)
	backdate()
	round()
	if got := firingKeys(); got != fmt.Sprintf("mount:%d:%s=critical,reach=critical", h[2], pool.UUID) {
		t.Fatalf("stale: %s", got)
	}
	events(2, 11)
	c, _ = load()
	resolveStorageAlarms(ctx, c)
	events(0, 13)
}

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

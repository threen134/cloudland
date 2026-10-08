/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Health of the storage clusters (shared-storage-design.md §14). Every minute the leader clapi asks one host of each
// ready cluster for its health: stc_health.sh runs the backend_health hook of the kind in the background and sends
// back a report that is the same JSON for every kind. The report updates the cluster, its hosts, disks and file
// systems, and raises or resolves the alarms of §14.2. The alarms on the pools (usage, a file system not mounted on a
// member) come from what the pool probes report, so the watchdog round evaluates them itself.

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Settings of the storage alarms, mirrored from cpgateway: the defaults and ranges must match its settings
const (
	StorageAlertDelaySetting        = "STORAGE_ALERT_DELAY_MINUTES"
	StoragePoolUsageWarnSetting     = "STORAGE_POOL_USAGE_WARN_PERCENT"
	StoragePoolUsageCriticalSetting = "STORAGE_POOL_USAGE_CRITICAL_PERCENT"
	DefaultStorageAlertDelayMinutes = 2
	DefaultStoragePoolUsageWarn     = 80
	DefaultStoragePoolUsageCritical = 90
)

// Alarms of the storage clusters (§14.2)
const (
	StorageAlarmClusterUnhealthy = "StorageClusterUnhealthy"
	StorageAlarmNodeDown         = "StorageNodeDown"
	StorageAlarmDiskDown         = "StorageDiskDown"
	StorageAlarmPoolUsageHigh    = "StoragePoolUsageHigh"
	StorageAlarmCephNearFull     = "CephNearFull"
	StorageAlarmFsUnmounted      = "StorageFsUnmounted"
)

// A cluster whose health report is older than this shows unknown health: the hosts that could check are away
const storageHealthStale = 5 * time.Minute

// StorageHealthReport is what stc_health.sh sends back, the same for every kind
type StorageHealthReport struct {
	// healthy | warning | error, or unknown when the host could not check (Error says why)
	Health   string   `json:"health"`
	Error    string   `json:"error,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Messages []string `json:"messages,omitempty"`
	// The hosts as the storage software sees them, by host name: active is fine, anything else is its own word
	Nodes []StorageHealthItem `json:"nodes,omitempty"`
	// The disks by the software's name (StorageClusterDisk.Name): up is fine
	Disks       []StorageHealthItem    `json:"disks,omitempty"`
	Filesystems []StorageHealthFs      `json:"filesystems,omitempty"`
	Capacity    *StorageHealthCapacity `json:"capacity,omitempty"`
	// Conditions of the whole cluster: nearfull (ceph)
	Flags []string `json:"flags,omitempty"`
}

type StorageHealthItem struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type StorageHealthFs struct {
	Name    string `json:"name"`
	Mounted bool   `json:"mounted"`
	Total   int64  `json:"total"`
	Free    int64  `json:"free"`
}

type StorageHealthCapacity struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

// StorageHealthInfo is kept in storage_clusters.health_info: the last report for display, and the state of the
// alarms (when each condition was first seen, which alarms fire with which severity)
type StorageHealthInfo struct {
	Summary  string                 `json:"summary,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Messages []string               `json:"messages,omitempty"`
	Flags    []string               `json:"flags,omitempty"`
	Capacity *StorageHealthCapacity `json:"capacity,omitempty"`
	// Host that sent the last report
	Hostid int32 `json:"hostid,omitempty"`
	// Condition key → when it was first seen
	Since map[string]time.Time `json:"since,omitempty"`
	// Condition key → the alarm that fires for it
	Firing map[string]*StorageAlarmState `json:"firing,omitempty"`
}

// StorageAlarmState is an alarm that fires for a condition
type StorageAlarmState struct {
	Name     string    `json:"name"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Since    time.Time `json:"since"`
	// When this alarm fired: a severity that comes back within one outage (critical, warning, critical again) is a
	// new event, not the first one fired again
	Fired time.Time `json:"fired"`
}

// storageCondition is something wrong right now: key names it within the cluster (cluster, node:<hostid>,
// disk:<name>, flag:nearfull, pool:<uuid>, mount:<hostid>:<pool uuid>); it raises an alarm once it lasted the delay,
// at once when immediate
type storageCondition struct {
	Key       string
	Name      string
	Severity  string
	Summary   string
	Immediate bool
}

// storageAlarmChange is an alarm to raise (Resolve false) or resolve
type storageAlarmChange struct {
	Key     string
	State   *StorageAlarmState
	Resolve bool
}

// ParseStorageHealthInfo reads storage_clusters.health_info; anything unreadable gives an empty one
func ParseStorageHealthInfo(raw string) *StorageHealthInfo {
	info := &StorageHealthInfo{}
	_ = json.Unmarshal([]byte(raw), info)
	if info.Since == nil {
		info.Since = map[string]time.Time{}
	}
	if info.Firing == nil {
		info.Firing = map[string]*StorageAlarmState{}
	}
	return info
}

// storageAlarmTransitions applies what is wrong now to the alarm state of a cluster, for the conditions whose keys
// start with one of prefixes (a report only speaks for its own part: the health report for the cluster, its hosts
// and disks, the pool round for the pools). A condition raises its alarm once it lasted delay; one that is gone
// clears its time and resolves its alarm; a severity that changes resolves the alarm and raises it again. A key in
// keep was not looked at this time (a host or disk missing from a report whose query failed): it stays as it is
func storageAlarmTransitions(info *StorageHealthInfo, prefixes []string, bad []storageCondition, keep map[string]bool, now time.Time,
	delay time.Duration) []storageAlarmChange {
	inScope := func(key string) bool {
		for _, p := range prefixes {
			if key == p || strings.HasPrefix(key, p+":") {
				return true
			}
		}
		return false
	}
	changes := []storageAlarmChange{}
	seen := map[string]bool{}
	for _, c := range bad {
		seen[c.Key] = true
		since, ok := info.Since[c.Key]
		if !ok {
			since = now
			info.Since[c.Key] = now
		}
		if !c.Immediate && now.Sub(since) < delay {
			continue
		}
		current := info.Firing[c.Key]
		if current != nil && current.Severity == c.Severity && current.Name == c.Name {
			continue
		}
		if current != nil {
			changes = append(changes, storageAlarmChange{Key: c.Key, State: current, Resolve: true})
		}
		state := &StorageAlarmState{Name: c.Name, Severity: c.Severity, Summary: c.Summary, Since: since, Fired: now}
		info.Firing[c.Key] = state
		changes = append(changes, storageAlarmChange{Key: c.Key, State: state})
	}
	keys := []string{}
	for k := range info.Since {
		keys = append(keys, k)
	}
	for k := range info.Firing {
		if _, ok := info.Since[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if seen[k] || keep[k] || !inScope(k) {
			continue
		}
		delete(info.Since, k)
		if state := info.Firing[k]; state != nil {
			changes = append(changes, storageAlarmChange{Key: k, State: state, Resolve: true})
			delete(info.Firing, k)
		}
	}
	return changes
}

func storageAlertDelay() time.Duration {
	m := GetMirrorSettingInt(StorageAlertDelaySetting, DefaultStorageAlertDelayMinutes)
	if m < 1 || m > 60 {
		m = DefaultStorageAlertDelayMinutes
	}
	return time.Duration(m) * time.Minute
}

// storagePoolUsageThresholds are the percents of a shared pool's capacity that raise a warning and a critical alarm
func storagePoolUsageThresholds() (warn, critical int) {
	warn = GetMirrorSettingInt(StoragePoolUsageWarnSetting, DefaultStoragePoolUsageWarn)
	if warn < 50 || warn > 99 {
		warn = DefaultStoragePoolUsageWarn
	}
	critical = GetMirrorSettingInt(StoragePoolUsageCriticalSetting, DefaultStoragePoolUsageCritical)
	if critical < 50 || critical > 100 {
		critical = DefaultStoragePoolUsageCritical
	}
	return
}

// storageHealthHost picks the host that checks a cluster: an admin host of a managed cluster, any member of an
// imported one; online, the first by host id, -1 when none is
func storageHealthHost(db *gorm.DB, cluster *model.StorageCluster, nodes []*model.StorageClusterNode) int32 {
	for _, n := range nodes {
		if n.Status == model.StorageNodeJoining || n.Status == model.StorageNodeLeaving {
			continue
		}
		if cluster.Mode == model.StorageModeManaged && !n.HasRole(model.StorageRoleAdmin) {
			continue
		}
		if _, online := hostOnline(db, n.Hostid); online {
			return n.Hostid
		}
	}
	return -1
}

// storageHealthInputer is a backend that adds kind specific input to the health check (the client user of an
// imported Ceph cluster)
type storageHealthInputer interface {
	HealthInput(cluster *model.StorageCluster) map[string]interface{}
}

// requestStorageHealth sends the health check of a cluster to one of its hosts, which reports with storage_health;
// -1 when no host can check
func requestStorageHealth(ctx context.Context, cluster *model.StorageCluster, nodes []*model.StorageClusterNode) int32 {
	db := dbs.DBContext(ctx)
	host := storageHealthHost(db, cluster, nodes)
	if host < 0 {
		return host
	}
	input := map[string]interface{}{"kind": cluster.Kind, "mode": cluster.Mode}
	fss := []map[string]string{}
	for _, f := range StorageClusters.Filesystems(ctx, cluster.ID) {
		fss = append(fss, map[string]string{"name": f.Name, "mount": f.MountPoint})
	}
	input["filesystems"] = fss
	if backend, err := storageBackendOf(cluster.Kind); err == nil {
		if hi, ok := backend.(storageHealthInputer); ok {
			for k, v := range hi.HealthInput(cluster) {
				input[k] = v
			}
		}
	}
	body, _ := json.Marshal(input)
	command := fmt.Sprintf("%s/stc_health.sh '%s' <<'EOF'\n%s\nEOF", storageScriptDir, ShellEscape(cluster.UUID), body)
	if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", host), command); err != nil {
		logger.Ctx(ctx).Warningf("Failed to ask host %d for the health of storage cluster %s: %v", host, cluster.Name, err)
	}
	return host
}

// storageUnreachable is the condition of a cluster nobody can check: no host can ask it, or it does not answer. The
// moment that matters most (the monitors lost their quorum, GPFS is down everywhere) is also the one with no report
func storageUnreachable(cluster *model.StorageCluster, why string) storageCondition {
	return storageCondition{Key: "reach", Name: StorageAlarmClusterUnhealthy, Severity: "critical",
		Summary: truncate(fmt.Sprintf("Storage cluster %s can not be checked: %s", cluster.Name, why), 500)}
}

// maintainStorageHealth is the watchdog round of a cluster: the health check goes out, a report gone stale turns
// the health unknown (and, after the delay, raises the alarm of a cluster nobody can check), and the alarms of the
// pools are evaluated from what the probes reported
func maintainStorageHealth(ctx context.Context, cluster *model.StorageCluster, nodes []*model.StorageClusterNode) {
	host := requestStorageHealth(ctx, cluster, nodes)
	db := dbs.DBContext(ctx)
	var changes []storageAlarmChange
	var locked *model.StorageCluster
	err := db.Transaction(func(tx *gorm.DB) error {
		locked = &model.StorageCluster{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(locked, cluster.ID).Error; err != nil {
			return err
		}
		info := ParseStorageHealthInfo(locked.HealthInfo)
		updates := map[string]interface{}{}
		prefixes := []string{"pool", "mount"}
		stale := false
		if locked.HealthAt == nil || time.Since(*locked.HealthAt) > storageHealthStale {
			if locked.Health != model.StorageHealthUnknown {
				updates["health"] = model.StorageHealthUnknown
			}
			// A cluster that never reported yet is given the time of a report since it became ready
			stale = locked.HealthAt != nil || time.Since(locked.UpdatedAt) > storageHealthStale
		}
		bad, err := storagePoolConditions(tx, locked)
		if err != nil {
			return err
		}
		// Nobody can check it: the alarm of an unreachable cluster. A report that comes resolves it
		switch {
		case host < 0:
			prefixes = append(prefixes, "reach")
			bad = append(bad, storageUnreachable(locked, "no host that checks it is online"))
		case stale:
			prefixes = append(prefixes, "reach")
			bad = append(bad, storageUnreachable(locked, fmt.Sprintf("no health report for more than %s", storageHealthStale)))
		}
		changes = storageAlarmTransitions(info, prefixes, bad, nil, time.Now(), storageAlertDelay())
		raw, _ := json.Marshal(info)
		if string(raw) != locked.HealthInfo {
			updates["health_info"] = string(raw)
		}
		if len(updates) == 0 {
			return nil
		}
		return tx.Model(&model.StorageCluster{}).Where("id = ?", locked.ID).Updates(updates).Error
	})
	if err != nil {
		logger.Ctx(ctx).Warningf("Failed to evaluate the pool alarms of storage cluster %s: %v", cluster.Name, err)
		return
	}
	notifyStorageAlarms(ctx, locked, changes)
}

// storagePoolConditions are the pools of a cluster that are too full (§9.5 usage against the thresholds), and the
// file pools a member host does not reach (its file system not mounted: the probe of §9.2 says so)
func storagePoolConditions(tx *gorm.DB, cluster *model.StorageCluster) ([]storageCondition, error) {
	pools, err := sharedPoolsOfCluster(tx, cluster.ID)
	if err != nil {
		return nil, err
	}
	warn, critical := storagePoolUsageThresholds()
	bad := []storageCondition{}
	members := map[int32]bool{}
	nodes := []*model.StorageClusterNode{}
	if err := tx.Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageNodeActive).Find(&nodes).Error; err != nil {
		return nil, err
	}
	for _, n := range nodes {
		members[n.Hostid] = true
	}
	for _, p := range pools {
		if p.Status != model.StoragePoolActive {
			continue
		}
		pct := int(p.UsageRatio()*100 + 0.5)
		switch {
		case pct >= critical:
			bad = append(bad, storageCondition{Key: "pool:" + p.UUID, Name: StorageAlarmPoolUsageHigh, Severity: "critical", Immediate: true,
				Summary: fmt.Sprintf("Storage pool %s of cluster %s is %d%% full (critical at %d%%)", p.Name, cluster.Name, pct, critical)})
		case pct >= warn:
			bad = append(bad, storageCondition{Key: "pool:" + p.UUID, Name: StorageAlarmPoolUsageHigh, Severity: "warning", Immediate: true,
				Summary: fmt.Sprintf("Storage pool %s of cluster %s is %d%% full (warning at %d%%)", p.Name, cluster.Name, pct, warn)})
		}
		d, err := poolDriverOf(p)
		if err != nil || d.Family() != PoolFamilyFile {
			continue
		}
		rows := []*model.HyperStoragePool{}
		if err := tx.Where("pool_id = ?", p.ID).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !members[r.Hostid] || r.Status == model.HyperPoolReady {
				continue
			}
			// A host that is offline is the node alarm's business
			if _, online := hostOnline(tx, r.Hostid); !online {
				continue
			}
			bad = append(bad, storageCondition{Key: fmt.Sprintf("mount:%d:%s", r.Hostid, p.UUID), Name: StorageAlarmFsUnmounted, Severity: "critical",
				Summary: fmt.Sprintf("Storage pool %s of cluster %s is not usable on host %s: %s", p.Name, cluster.Name, hostName(tx, r.Hostid), r.Reason)})
		}
	}
	return bad, nil
}

// storageShortName is a host name without its domain, as the storage software and the hosts may write it either way
func storageShortName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.Index(name, "."); i > 0 {
		name = name[:i]
	}
	return name
}

// HandleStorageHealth takes the health report of a cluster from the host that checked it
func HandleStorageHealth(ctx context.Context, hostid int32, clusterUUID string, report *StorageHealthReport) error {
	db := dbs.DBContext(ctx)
	var changes []storageAlarmChange
	var cluster *model.StorageCluster
	err := db.Transaction(func(tx *gorm.DB) error {
		cluster = &model.StorageCluster{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ?", clusterUUID).Take(cluster).Error; err != nil {
			return fmt.Errorf("storage cluster %s not found", clusterUUID)
		}
		nodes := []*model.StorageClusterNode{}
		if err := tx.Where("cluster_id = ?", cluster.ID).Find(&nodes).Error; err != nil {
			return err
		}
		member := false
		for _, n := range nodes {
			member = member || n.Hostid == hostid
		}
		if !member {
			return fmt.Errorf("host %d reported the health of storage cluster %s, which it is not in", hostid, cluster.Name)
		}
		now := time.Now()
		info := ParseStorageHealthInfo(cluster.HealthInfo)
		info.Hostid = hostid
		info.Error = report.Error
		updates := map[string]interface{}{"health_at": &now}
		health := report.Health
		switch health {
		case model.StorageHealthHealthy, model.StorageHealthWarning, model.StorageHealthError:
		default:
			// The host could not check: the other alarms keep their state until a host can tell, and the cluster
			// raises the alarm of one nobody can check once that lasted the delay
			updates["health"] = model.StorageHealthUnknown
			why := report.Error
			if why == "" {
				why = "the check gave no answer"
			}
			changes = storageAlarmTransitions(info, []string{"reach"}, []storageCondition{storageUnreachable(cluster, why)}, nil, now, storageAlertDelay())
			raw, _ := json.Marshal(info)
			updates["health_info"] = string(raw)
			return tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Updates(updates).Error
		}
		updates["health"] = health
		info.Summary, info.Messages, info.Flags, info.Capacity = report.Summary, report.Messages, report.Flags, report.Capacity
		if len(info.Messages) > 20 {
			info.Messages = info.Messages[:20]
		}
		bad := []storageCondition{}
		if health != model.StorageHealthHealthy {
			severity := "warning"
			if health == model.StorageHealthError {
				severity = "critical"
			}
			summary := fmt.Sprintf("Storage cluster %s is %s", cluster.Name, health)
			if len(report.Messages) > 0 {
				summary += ": " + strings.Join(report.Messages[:min(len(report.Messages), 3)], "; ")
			}
			bad = append(bad, storageCondition{Key: "cluster", Name: StorageAlarmClusterUnhealthy, Severity: severity, Summary: truncate(summary, 500)})
		}
		for _, f := range report.Flags {
			if f == "nearfull" {
				bad = append(bad, storageCondition{Key: "flag:nearfull", Name: StorageAlarmCephNearFull, Severity: "critical", Immediate: true,
					Summary: fmt.Sprintf("Ceph cluster %s is near full: writes stop when an OSD reaches the full ratio", cluster.Name)})
			}
		}
		// The hosts: by name, as the software reports them
		hostOf := map[string]int32{}
		for _, n := range nodes {
			hostOf[storageShortName(hostName(tx, n.Hostid))] = n.Hostid
		}
		stateOf := map[int32]string{}
		for _, item := range report.Nodes {
			if h, ok := hostOf[storageShortName(item.Name)]; ok {
				stateOf[h] = item.State
			}
		}
		// A host or disk the report leaves out (its query timed out, the software does not list it) keeps its alarm
		// state: missing is not the same as recovered
		keep := map[string]bool{}
		for _, n := range nodes {
			state, reported := stateOf[n.Hostid]
			if !reported {
				keep[fmt.Sprintf("node:%d", n.Hostid)] = true
				continue
			}
			if err := tx.Model(&model.StorageClusterNode{}).Where("id = ?", n.ID).Updates(map[string]interface{}{"state": truncate(state, 64), "checked_at": &now}).Error; err != nil {
				return err
			}
			if n.Status == model.StorageNodeActive && state != "active" {
				bad = append(bad, storageCondition{Key: fmt.Sprintf("node:%d", n.Hostid), Name: StorageAlarmNodeDown, Severity: "warning",
					Summary: fmt.Sprintf("Host %s of storage cluster %s is %s", hostName(tx, n.Hostid), cluster.Name, state)})
			}
		}
		// The disks: by the software's name
		disks := []*model.StorageClusterDisk{}
		if err := tx.Where("cluster_id = ? AND name <> ''", cluster.ID).Find(&disks).Error; err != nil {
			return err
		}
		diskState := map[string]string{}
		for _, item := range report.Disks {
			diskState[item.Name] = item.State
		}
		alarmed := map[string]bool{}
		for _, d := range disks {
			state, reported := diskState[d.Name]
			if !reported {
				keep["disk:"+d.Name] = true
				continue
			}
			if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Updates(map[string]interface{}{"state": truncate(state, 32), "checked_at": &now}).Error; err != nil {
				return err
			}
			// A shared LUN is one disk however many hosts serve it: one condition
			if d.Status == model.StorageDiskActive && state != "up" && !alarmed[d.Name] {
				alarmed[d.Name] = true
				bad = append(bad, storageCondition{Key: "disk:" + d.Name, Name: StorageAlarmDiskDown, Severity: "warning",
					Summary: fmt.Sprintf("Disk %s (%s on %s) of storage cluster %s is %s", d.Name, d.DiskID, hostName(tx, d.Hostid), cluster.Name, state)})
			}
		}
		for _, f := range report.Filesystems {
			if !f.Mounted || f.Total <= 0 {
				continue
			}
			if err := tx.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND name = ?", cluster.ID, f.Name).
				Updates(map[string]interface{}{"capacity_bytes": f.Total, "free_bytes": f.Free, "capacity_at": &now}).Error; err != nil {
				return err
			}
		}
		changes = storageAlarmTransitions(info, []string{"reach", "cluster", "flag", "node", "disk"}, bad, keep, now, storageAlertDelay())
		raw, _ := json.Marshal(info)
		updates["health_info"] = string(raw)
		return tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	notifyStorageAlarms(ctx, cluster, changes)
	return nil
}

// resolveStorageAlarms resolves every alarm of a cluster that goes away: no report would ever resolve them
func resolveStorageAlarms(ctx context.Context, cluster *model.StorageCluster) {
	info := ParseStorageHealthInfo(cluster.HealthInfo)
	changes := []storageAlarmChange{}
	for k, state := range info.Firing {
		changes = append(changes, storageAlarmChange{Key: k, State: state, Resolve: true})
	}
	notifyStorageAlarms(ctx, cluster, changes)
}

// notifyStorageAlarms records and sends the alarm changes of a cluster in the background, like the VPN alarms: the
// events belong to the system organization and go to its notification channels
func notifyStorageAlarms(ctx context.Context, cluster *model.StorageCluster, changes []storageAlarmChange) {
	if len(changes) == 0 || cluster == nil {
		return
	}
	bg := SetContextDB(context.WithoutCancel(ctx), DB())
	c := *cluster
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Ctx(bg).Errorf("Storage alarm notification panic: %v", r)
			}
		}()
		sendStorageAlarms(bg, &c, changes, time.Now())
	}()
}

func storageAlarmLabels(ctx context.Context, cluster *model.StorageCluster, change storageAlarmChange) map[string]string {
	return map[string]string{
		"alertname":       change.State.Name,
		"owner":           strconv.FormatInt(platformOrgID(ctx), 10),
		"severity":        change.State.Severity,
		"summary":         change.State.Summary,
		"vm_name":         fmt.Sprintf("%s / %s", cluster.Name, change.Key),
		"vm_uuid":         cluster.UUID,
		"storage_cluster": cluster.UUID,
		"storage_alarm":   cluster.UUID + "|" + change.Key + "|" + change.State.Name,
		"source":          "storage",
	}
}

func sendStorageAlarms(ctx context.Context, cluster *model.StorageCluster, changes []storageAlarmChange, at time.Time) {
	ctx, db := GetContextDB(ctx)
	admin := &NotificationAdmin{}
	type pending struct {
		event      *model.AlarmEvent
		notifyType string
	}
	todo := []pending{}
	for _, change := range changes {
		labels := storageAlarmLabels(ctx, cluster, change)
		if !change.Resolve {
			// A fresh fingerprint per firing (the upsert only fires events it has not seen, and one it has seen keeps
			// its row: a severity that comes back must not take over the row of its first time), short enough for the
			// column
			fired := change.State.Fired
			if fired.IsZero() {
				fired = change.State.Since
			}
			sum := sha1.Sum([]byte(labels["storage_alarm"] + "|" + change.State.Severity))
			fingerprint := fmt.Sprintf("storage-%x-%d", sum[:8], fired.UnixNano())
			event, notifyType, err := admin.UpsertAlarmEvent(ctx, fingerprint, labels, "firing", at)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to record storage alarm event: %v", err)
				continue
			}
			if notifyType != "" {
				todo = append(todo, pending{event, notifyType})
			}
			continue
		}
		firing := []*model.AlarmEvent{}
		if err := db.Where("alert_name = ? AND status = ? AND labels LIKE ?", change.State.Name, "firing", "%"+labels["storage_alarm"]+"%").
			Find(&firing).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query storage alarm events: %v", err)
			continue
		}
		for _, existing := range firing {
			event, notifyType, err := admin.UpsertAlarmEvent(ctx, existing.Fingerprint, labels, "resolved", at)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to resolve storage alarm event: %v", err)
				continue
			}
			if notifyType != "" {
				todo = append(todo, pending{event, notifyType})
			}
		}
	}
	if len(todo) == 0 {
		return
	}
	channels, err := admin.EnabledChannelsOfOrg(ctx, platformOrgID(ctx))
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to query notification channels: %v", err)
		return
	}
	notifier := NewAlarmNotifier()
	for _, item := range todo {
		for _, channel := range channels {
			result := notifier.SendNotification(ctx, channel, item.event, item.notifyType)
			entry := &model.AlarmDeliveryLog{
				EventUUID: item.event.UUID, ChannelUUID: result.ChannelUUID, ChannelName: result.ChannelName, ChannelType: result.ChannelType,
				NotifyType: item.notifyType, Status: result.Status, ErrorMessage: result.Error, SentAt: time.Now(),
			}
			if err := admin.CreateDeliveryLog(ctx, entry); err != nil {
				logger.Ctx(ctx).Errorf("Failed to record storage notification delivery: %v", err)
			}
		}
	}
}

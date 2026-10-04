/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Ceph (RBD, deployed with cephadm) as a storage backend: shared-storage-design.md §8

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageBackend(cephBackend{})
}

type cephBackend struct{}

const (
	// The RBD user CloudLand makes on a managed cluster (§8.2 step 9)
	cephClientUser = "cloudland"
	// Where every host keeps the client configuration of a cluster (§6.8)
	cephConfDir = "/etc/ceph"
)

var (
	cephImageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_@-]{0,199}$`)
	cephUserRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	cephKeyRe   = regexp.MustCompile(`^[A-Za-z0-9+/]{38,64}={0,2}$`)
	cephPoolRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	cephPortRe  = regexp.MustCompile(`^[0-9]{2,5}$`)
)

// cephParams are the parameters of a Ceph cluster (§8.2)
type cephParams struct {
	storageCommonParams
	Replicas           int `json:"replicas,omitempty"`
	OsdMemoryTargetMiB int `json:"osd_memory_target_mib,omitempty"`
	// Container image of the daemons; empty: quay.io/ceph/ceph of the release the hosts install from their
	// distribution (§8.1)
	Image string `json:"image,omitempty"`
	// Network of the replication traffic between the OSDs; empty: the public network
	ClusterNetwork string `json:"cluster_network,omitempty"`
}

func (cephBackend) Kind() string { return model.StorageKindCeph }

func (cephBackend) Roles() []string {
	return []string{model.StorageRoleAdmin, model.StorageRoleMon, model.StorageRoleMgr, model.StorageRoleOSD, model.StorageRoleClient}
}

func (cephBackend) DiskRole() string { return model.StorageRoleOSD }

func (cephBackend) DefaultRoles() (base, once []string) {
	return []string{model.StorageRoleMon, model.StorageRoleOSD}, []string{model.StorageRoleAdmin, model.StorageRoleMgr}
}

var cephRequirements = &StorageRequirements{
	Systems: []StorageOSRule{
		{ID: "ubuntu", Version: "24.04"},
		{ID: "ubuntu", Version: "26.04"},
	},
	// mon v2, mon v1 and the first port of the OSD range
	PeerPorts: []int{22, 3300, 6789, 6800},
	// a mon stops below 5% free room and warns below 30% (§3.1)
	DataDirs:   []storageDataDir{{Path: "/var/lib/ceph", MinFreePercent: 30, Roles: []string{model.StorageRoleMon}}},
	Containers: true,
}

func (cephBackend) Requirements() *StorageRequirements { return cephRequirements }

func (b cephBackend) ParseParams(raw json.RawMessage) (interface{}, error) {
	p := &cephParams{}
	if err := decodeStorageParams(b.Kind(), raw, p); err != nil {
		return nil, err
	}
	if err := storageParamRange(b.Kind(), "replicas", p.Replicas, 1, 3); err != nil {
		return nil, err
	}
	if err := storageParamRange(b.Kind(), "osd_memory_target_mib", p.OsdMemoryTargetMiB, 896, 65536); err != nil {
		return nil, err
	}
	if p.Image != "" && !cephImageRe.MatchString(p.Image) {
		return nil, planError("The image of a Ceph cluster is a container image reference such as quay.io/ceph/ceph:v19.2.3")
	}
	if p.ClusterNetwork != "" {
		if _, _, err := net.ParseCIDR(p.ClusterNetwork); err != nil || strings.Contains(p.ClusterNetwork, ":") {
			return nil, planError("The cluster network of a Ceph cluster is an IPv4 network such as 10.0.1.0/24")
		}
	}
	return p, nil
}

func (cephBackend) CheckLayout(nodes []*StorageNodePlan, disks []*StorageDiskPlan, params interface{}) error {
	p := params.(*cephParams)
	mon, mgr, admins, osdHosts := 0, 0, 0, 0
	disksOn := map[int32]int{}
	for _, d := range disks {
		disksOn[d.Hostid]++
	}
	for _, node := range nodes {
		if hasRole(node.Roles, model.StorageRoleMon) {
			mon++
		}
		if hasRole(node.Roles, model.StorageRoleMgr) {
			mgr++
		}
		if hasRole(node.Roles, model.StorageRoleAdmin) {
			admins++
			if !hasRole(node.Roles, model.StorageRoleMon) {
				return planError("An admin host must also be a mon host")
			}
		}
		if hasRole(node.Roles, model.StorageRoleOSD) {
			if disksOn[node.Hostid] == 0 {
				return planError("An OSD host needs at least one disk")
			}
			osdHosts++
		}
	}
	if mon%2 == 0 || (mon == 1 && !p.Test) {
		return planError("A Ceph cluster needs an odd number of mon hosts, at least 3 (1 only in the test layout); %d given", mon)
	}
	if mgr < 1 || mgr > 2 {
		return planError("A Ceph cluster needs 1 or 2 mgr hosts; %d given", mgr)
	}
	if admins < 1 {
		return planError("A Ceph cluster needs an admin host")
	}
	// size 2 either writes a single copy (min_size 1) or stops writing when a host is lost (min_size 2) (§6.3)
	replicas := cephReplicas(p)
	if p.Replicas != 0 && p.Replicas != replicas {
		return planError("A managed Ceph cluster keeps 3 replicas (1 only in the test layout)")
	}
	if osdHosts < replicas {
		return planError("A Ceph cluster with %d replicas needs OSDs on at least %d hosts; %d given", replicas, replicas, osdHosts)
	}
	return nil
}

// cephReplicas is the size of the pools of a cluster: 3, or 1 in the test layout
func cephReplicas(p *cephParams) int {
	if p.Test {
		return 1
	}
	return 3
}

// Two clusters' mons fight over the same ports, so a host runs the daemons of one Ceph cluster; as a client it can use
// any number (§4.1)
func (cephBackend) HostConflict(roles, otherRoles []string) string {
	if storageDaemonRoles(roles) && storageDaemonRoles(otherRoles) {
		return "runs the daemons of another Ceph cluster already"
	}
	return ""
}

func (cephBackend) DefaultLayout() string { return "" }

// Ceph needs no attributes per host or disk to start with; the OSD id of a disk is recorded once it is made
func (cephBackend) InitialAttrs(existing []*model.StorageClusterNode, existingDisks []*model.StorageClusterDisk, nodes []*StorageNodePlan,
	disks []*StorageDiskPlan) (map[int32]map[string]interface{}, map[string]map[string]interface{}) {
	return nil, nil
}

// mon 2 GiB, mgr 1 GiB, every OSD its memory target + 0.5 GiB; a client takes nothing, librbd counts in QEMU (§6.7)
func (cephBackend) ReserveMB(roles []string, disks int, params interface{}) int32 {
	var mb int32
	if hasRole(roles, model.StorageRoleMon) {
		mb += 2048
	}
	if hasRole(roles, model.StorageRoleMgr) {
		mb += 1024
	}
	return mb + int32(disks)*int32(cephOsdMemoryMiB(params.(*cephParams))+512)
}

func cephOsdMemoryMiB(p *cephParams) int {
	if p.OsdMemoryTargetMiB <= 0 {
		return 2048
	}
	return p.OsdMemoryTargetMiB
}

// Ceph rebalances by itself when OSDs come and go (§8.5): there is no rebalance to start, and no file system layer
func (cephBackend) Capabilities() *StorageCapabilities {
	return &StorageCapabilities{Managed: true, External: true, Pools: true, AddNodes: true, RemoveNode: true, AddDisks: true, RemoveDisk: true,
		ReplaceDisk: true, ChangeRoles: true}
}

func (cephBackend) PoolDriver() string { return model.StorageDriverCephRBD }

// cephImportParams are what CloudLand must know to use a Ceph cluster its admins run (§8.8)
type cephImportParams struct {
	Fsid       string   `json:"fsid"`
	MonAddrs   []string `json:"mon_addrs"`
	ClientUser string   `json:"client_user"`
	ClientKey  string   `json:"client_key,omitempty"`
}

func (b cephBackend) ParseImportParams(raw json.RawMessage) (interface{}, error) {
	p := &cephImportParams{}
	if err := decodeStorageParams(b.Kind(), raw, p); err != nil {
		return nil, err
	}
	p.Fsid = strings.ToLower(strings.TrimSpace(p.Fsid))
	if !validUUID(p.Fsid) {
		return nil, planError("Give the fsid of the Ceph cluster (ceph fsid)")
	}
	mons, err := cephMonAddrs(p.MonAddrs)
	if err != nil {
		return nil, err
	}
	p.MonAddrs = mons
	if !cephUserRe.MatchString(p.ClientUser) || strings.HasPrefix(p.ClientUser, "client.") {
		return nil, planError("Give the client user CloudLand works as, without the client. prefix (it needs mon 'profile rbd' and osd 'profile rbd pool=...')")
	}
	if !cephKeyRe.MatchString(p.ClientKey) {
		return nil, planError("Give the key of the client user (ceph auth get-key client.<user>)")
	}
	return p, nil
}

// cephMonAddrs checks mon addresses given as ip or ip:port, the port 6789 (msgr v1) when left out
func cephMonAddrs(addrs []string) ([]string, error) {
	out := []string{}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		host, port := a, "6789"
		if strings.Contains(a, ":") {
			var err error
			if host, port, err = net.SplitHostPort(a); err != nil {
				return nil, planError("Mon address %s is not ip or ip:port", a)
			}
		}
		if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
			return nil, planError("Mon address %s is not an IPv4 address", a)
		}
		if !cephPortRe.MatchString(port) {
			return nil, planError("Mon address %s has no valid port", a)
		}
		out = append(out, host+":"+port)
	}
	if len(out) == 0 || len(out) > 9 {
		return nil, planError("Give 1 to 9 mon addresses of the Ceph cluster")
	}
	return out, nil
}

// SplitImportSecrets keeps the client key out of the parameters: it is stored encrypted with the cluster (§5.10)
func (cephBackend) SplitImportSecrets(params interface{}) (interface{}, map[string]string) {
	p := *params.(*cephImportParams)
	secrets := map[string]string{"client_key": p.ClientKey}
	p.ClientKey = ""
	return &p, secrets
}

// storageImportSecrets is a backend whose import parameters carry credentials: they go to storage_clusters.secrets,
// encrypted, never to params
type storageImportSecrets interface {
	SplitImportSecrets(params interface{}) (public interface{}, secrets map[string]string)
}

// cephClusterInfo is what the hosts need to reach a cluster as its RBD client: from the parameters of an imported
// cluster, from what the deployment found out (storage_clusters.attrs) for a managed one
type cephClusterInfo struct {
	Fsid       string   `json:"fsid"`
	MonAddrs   []string `json:"mon_addrs"`
	ClientUser string   `json:"client_user"`
	// libvirt secret holding the client key on every host, the same uuid everywhere so a live migration finds it
	SecretUUID string `json:"secret_uuid"`
	// Container image and release the cluster runs (managed)
	Image   string `json:"image,omitempty"`
	Version string `json:"version,omitempty"`
}

func cephInfoOf(cluster *model.StorageCluster) *cephClusterInfo {
	info := &cephClusterInfo{}
	_ = json.Unmarshal([]byte(cluster.Attrs), info)
	if cluster.Mode == model.StorageModeExternal {
		p := &cephImportParams{}
		_ = json.Unmarshal([]byte(cluster.Params), p)
		info.Fsid, info.MonAddrs, info.ClientUser = p.Fsid, p.MonAddrs, p.ClientUser
	}
	// A managed cluster's fsid is the uuid of its record (§8.2 step 6), its client user CloudLand's
	if info.Fsid == "" && cluster.Mode == model.StorageModeManaged {
		info.Fsid = cluster.UUID
	}
	if info.ClientUser == "" {
		info.ClientUser = cephClientUser
	}
	if info.SecretUUID == "" {
		info.SecretUUID = cluster.UUID
	}
	return info
}

// HealthInput: a managed cluster is checked as client.admin on one of its admin hosts, an imported one as its client
// user (shared-storage-design.md §14.1)
func (cephBackend) HealthInput(cluster *model.StorageCluster) map[string]interface{} {
	info := cephInfoOf(cluster)
	return map[string]interface{}{"fsid": info.Fsid, "client_user": info.ClientUser, "conf": cephConfPath(cluster)}
}

// cephConfPath is the client configuration of a cluster on every host
func cephConfPath(cluster *model.StorageCluster) string {
	return cephConfDir + "/" + cluster.UUID + ".conf"
}

// cephClientKey reads the client key kept encrypted with the cluster
func cephClientKey(cluster *model.StorageCluster) (string, error) {
	secrets, err := storageClusterSecrets(cluster)
	if err != nil {
		return "", err
	}
	if secrets["client_key"] == "" {
		return "", NewCLError(ErrSecretUnavailable, "The cluster has no client key yet", nil)
	}
	return secrets["client_key"], nil
}

// storageClusterSecrets decrypts the kind specific credentials of a cluster
func storageClusterSecrets(cluster *model.StorageCluster) (map[string]string, error) {
	secrets := map[string]string{}
	if cluster.Secrets == "" {
		return secrets, nil
	}
	plain, err := DecryptSecret(cluster.Secrets)
	if err != nil {
		return nil, NewCLError(ErrSecretUnavailable, "The credentials of the cluster can not be decrypted", err)
	}
	if err := json.Unmarshal([]byte(plain), &secrets); err != nil {
		return nil, NewCLError(ErrSecretUnavailable, "The credentials of the cluster are unreadable", err)
	}
	return secrets, nil
}

// encryptStorageSecrets encrypts credentials for storage_clusters.secrets
func encryptStorageSecrets(secrets map[string]string) (string, error) {
	plain, err := json.Marshal(secrets)
	if err != nil {
		return "", err
	}
	return EncryptSecret(string(plain))
}

// PoolSetup: the RBD pool of a CloudLand pool on a managed cluster is cl_<uuid prefix>, its placement rule the one of
// the media asked for; on an imported cluster an existing pool is registered (§8.3)
func (b cephBackend) PoolSetup(tx *gorm.DB, cluster *model.StorageCluster, pool *model.StoragePool, raw json.RawMessage) error {
	info := cephInfoOf(cluster)
	params := map[string]interface{}{"cluster": cluster.UUID, "conf": cephConfPath(cluster), "user": info.ClientUser,
		"secret_uuid": info.SecretUUID}
	if cluster.Mode == model.StorageModeExternal {
		p := struct {
			CephPool string `json:"ceph_pool"`
		}{}
		if err := decodeStorageParams(b.Kind(), raw, &p); err != nil {
			return err
		}
		if !cephPoolRe.MatchString(p.CephPool) {
			return planError("Give the name of the existing RBD pool to use (ceph_pool)")
		}
		// The name may hold _, a LIKE wildcard: matched literally
		var taken int64
		if err := tx.Model(&model.StoragePool{}).Scopes(dbs.Contains(`"ceph_pool":"`+p.CephPool+`"`, "driver_params")).
			Where("cluster_id = ? AND id <> ?", cluster.ID, pool.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return planError("Another pool uses RBD pool %s already", p.CephPool)
		}
		params["ceph_pool"], params["external"] = p.CephPool, true
		pool.CloneMode = "copy"
	} else {
		if err := decodeStorageParams(b.Kind(), raw, &struct{}{}); err != nil {
			return err
		}
		cp := &cephParams{}
		_ = json.Unmarshal([]byte(cluster.Params), cp)
		replicas := cephReplicas(cp)
		rule := "replicated_rule"
		if pool.Media != "" {
			rule = "cl-" + pool.Media
		}
		// Replicas go to different hosts: the media needs OSDs on as many hosts (§8.3)
		var hosts int64
		q := tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageDiskActive)
		if pool.Media != "" {
			q = q.Where("media = ?", pool.Media)
		}
		q.Distinct("hostid").Count(&hosts)
		if int(hosts) < replicas {
			if pool.Media != "" {
				return planError("Storage cluster %s has %s OSDs on %d hosts; a pool with %d replicas needs %d", cluster.Name, pool.Media, hosts, replicas, replicas)
			}
			return planError("Storage cluster %s has OSDs on %d hosts; a pool with %d replicas needs %d", cluster.Name, hosts, replicas, replicas)
		}
		params["ceph_pool"], params["crush_rule"] = "cl_"+pool.UUID[:8], rule
		pool.Replicas = int32(replicas)
		pool.CloneMode = "clone"
	}
	b2, _ := json.Marshal(params)
	pool.DriverParams = string(b2)
	pool.MountPath = ""
	return nil
}

// cephMgrMetricsPort is where the prometheus module of a Ceph mgr listens (only the active mgr gives data)
const cephMgrMetricsPort = 9283

// ScrapeTargets are the mgr hosts of a managed Ceph cluster: their prometheus module is switched on by the deployment
// and the health watchdog (§14.3). The mgrs of an imported cluster are not known to CloudLand
func (cephBackend) ScrapeTargets(cluster *model.StorageCluster, nodes []*model.StorageClusterNode, hosts map[int32]*model.Hyper) []SDTarget {
	targets := []SDTarget{}
	if cluster.Mode != model.StorageModeManaged {
		return targets
	}
	for _, n := range nodes {
		h := hosts[n.Hostid]
		if h == nil || h.HostIP == "" || !n.HasRole(model.StorageRoleMgr) || n.Status == model.StorageNodeJoining {
			continue
		}
		targets = append(targets, SDTarget{Targets: []string{hostPort(h, cephMgrMetricsPort)}, Labels: map[string]string{"hostname": h.Hostname}})
	}
	return targets
}

// MetricQueries are the curves of a Ceph cluster from its mgr prometheus module (appendix E.2), labelled with the
// cluster by the scrape target. Only the active mgr exports, so the sums and maxima are over one of them
func (cephBackend) MetricQueries(cluster *model.StorageCluster, window int64) []StorageMetricQuery {
	c := "storage_cluster=" + promLabel(cluster.UUID)
	rate := func(metric string) string {
		return fmt.Sprintf(`sum(rate(%s{%s}[%ds]))`, metric, c, window)
	}
	return []StorageMetricQuery{
		{Chart: StorageChartCapacity, Series: "used", Query: fmt.Sprintf(`max(ceph_cluster_total_used_bytes{%s})`, c)},
		{Chart: StorageChartCapacity, Series: "total", Query: fmt.Sprintf(`max(ceph_cluster_total_bytes{%s})`, c)},
		{Chart: StorageChartThroughput, Series: "read", Query: rate("ceph_pool_rd_bytes")},
		{Chart: StorageChartThroughput, Series: "write", Query: rate("ceph_pool_wr_bytes")},
		{Chart: StorageChartIOPS, Series: "read", Query: rate("ceph_pool_rd")},
		{Chart: StorageChartIOPS, Series: "write", Query: rate("ceph_pool_wr")},
		{Chart: StorageChartOSDs, Series: "up", Query: fmt.Sprintf(`sum(ceph_osd_up{%s})`, c)},
		{Chart: StorageChartOSDs, Series: "in", Query: fmt.Sprintf(`sum(ceph_osd_in{%s})`, c)},
	}
}

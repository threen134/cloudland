/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Storage backends (shared-storage-design.md §4.5): the common framework (task engine, disk claims, precheck, SSH
// keys, memory reservation) knows nothing about GPFS or Ceph. Everything it needs to know about a kind of storage
// comes through a StorageBackend, one file per kind (storage_backend_<kind>.go). Methods are added in the stage that
// first needs them; the full list and the questions every kind must answer are in §4.5.

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

// StorageBackend is a kind of storage cluster
type StorageBackend interface {
	Kind() string
	// Roles a member host may have, in the order forms show them. Every kind has model.StorageRoleClient: a host
	// that only uses the storage
	Roles() []string
	// DiskRole is the role of the hosts that give disks to the cluster; empty when the kind takes no disks
	DiskRole() string
	// DefaultRoles suggests the roles of one more host in a form: base for every host, plus each of once while no
	// host has it yet
	DefaultRoles() (base, once []string)
	// Requirements are what the hosts must have before anything is installed, checked by the precheck (§6.5)
	Requirements() *StorageRequirements
	// ParseParams reads and checks the kind's parameters (storage_clusters.params); empty input gives the defaults.
	// The value is only handed back to the other methods of the same backend
	ParseParams(raw json.RawMessage) (interface{}, error)
	// CheckLayout applies the role rules of the kind to the hosts and disks of a new cluster (§6.3)
	CheckLayout(nodes []*StorageNodePlan, disks []*StorageDiskPlan, params interface{}) error
	// HostConflict tells why a host with roles can not join a cluster while it is in another cluster of the same
	// kind with otherRoles (§4.1); empty when it can
	HostConflict(roles, otherRoles []string) string
	// ReserveMB is the memory the daemons of the kind take on a host with roles and disks (§6.7)
	ReserveMB(roles []string, disks int, params interface{}) int32
	// DefaultLayout is the layout of a new managed cluster (storage_clusters.layout): gpfs replica, ceph none
	DefaultLayout() string
	// InitialAttrs gives the kind specific attributes of the hosts and disks a cluster gets (the GPFS failure groups,
	// the usage and storage pool of every NSD), keyed by hostid and by "<hostid>/<disk id>": every host and disk of
	// a new cluster (existing nil), or what an expansion adds to the existing ones. A host of existing keeps its
	// attributes and is left out
	InitialAttrs(existing []*model.StorageClusterNode, existingDisks []*model.StorageClusterDisk, nodes []*StorageNodePlan,
		disks []*StorageDiskPlan) (nodeAttrs map[int32]map[string]interface{}, diskAttrs map[string]map[string]interface{})
	// TaskPlan lays out the steps of a task on a cluster of the kind (StorageTaskDeploy, StorageTaskDeleteCluster,
	// the pool tasks...); the steps are the ones the backend registered as "<kind>:<task>"
	TaskPlan(task string, cluster *model.StorageCluster, scope *StorageTaskScope) ([]*StorageStepPlan, error)
	// Capabilities tells which operations the kind supports; the API refuses the others and the interface does
	// not offer them
	Capabilities() *StorageCapabilities
	// PoolDriver is the driver of the CloudLand pools on clusters of the kind (storage_pools.driver)
	PoolDriver() string
	// ParseImportParams reads and checks the parameters of an imported cluster (what CloudLand must know to use it)
	ParseImportParams(raw json.RawMessage) (interface{}, error)
	// PoolSetup checks the kind specific parameters of a new pool and fills what the driver needs (file system,
	// driver parameters, root), in the transaction that records the pool with its id and uuid
	PoolSetup(tx *gorm.DB, cluster *model.StorageCluster, pool *model.StoragePool, params json.RawMessage) error
}

// StorageTaskScope is what a task is planned on: the hosts and disks of the cluster as the task will find them, those
// it brings marked joining / claiming, those it takes away leaving / removing
type StorageTaskScope struct {
	Nodes []*model.StorageClusterNode
	Disks []*model.StorageClusterDisk
	// The host leaving is gone for good: nothing runs on it, its disks are dropped without moving their data
	Offline bool
	// The host whose roles change (change_roles) and its roles before; the roles in Nodes are the new ones
	Changed     int32
	ChangedFrom []string
	// What a rotation renews (rotate_keys): the SSH key of the cluster, the key of its client user
	RotateSSH    bool
	RotateClient bool
	// What an upgrade goes to (upgrade)
	Upgrade *storageUpgradeParams
}

// StorageCapabilities are the operations a kind supports beyond its precheck (§4.5.1)
type StorageCapabilities struct {
	// Managed: CloudLand deploys and deletes clusters of the kind; External: it imports clusters set up by others
	Managed  bool `json:"managed"`
	External bool `json:"external"`
	// Filesystems: the kind has file systems between the cluster and the pools (gpfs)
	Filesystems bool `json:"filesystems"`
	// Pools: CloudLand pools can be made on clusters of the kind
	Pools      bool `json:"pools"`
	AddNodes   bool `json:"add_nodes"`
	RemoveNode bool `json:"remove_node"`
	AddDisks   bool `json:"add_disks"`
	RemoveDisk bool `json:"remove_disk"`
	Rebalance  bool `json:"rebalance"`
	// ReplaceDisk swaps a failed disk for a new disk of the same host; ChangeRoles changes the roles of a member
	ReplaceDisk bool `json:"replace_disk"`
	ChangeRoles bool `json:"change_roles"`
	// RotateKeys renews the SSH key of a managed cluster; ClientKey: the kind has a client key CloudLand renews too
	RotateKeys bool `json:"rotate_keys"`
	ClientKey  bool `json:"client_key"`
	// Upgrade: a rolling upgrade of the software of a managed cluster; Finalize: the kind raises the cluster to a new
	// release in a separate, irreversible step afterwards (gpfs)
	Upgrade  bool `json:"upgrade"`
	Finalize bool `json:"finalize"`
}

// StorageRequirements are what a kind needs on its hosts
type StorageRequirements struct {
	// Systems is the support matrix kept with CloudLand (§2.2)
	Systems []StorageOSRule
	// PeerPorts the members must reach on each other
	PeerPorts []int
	// DataDirs are where the daemons keep their data and how much room they need there
	DataDirs []storageDataDir
	// KernelModule: the kind builds a kernel module, so the headers of the running kernel must be installable
	KernelModule bool
	// Containers: the kind runs its daemons in containers, so docker or podman must be running
	Containers bool
	// Package: the software comes from an installer uploaded to the package repository (§6.1), not from the
	// distribution
	Package bool
}

// storageCommonParams are the parameters every kind has; each backend embeds them in its own params
type storageCommonParams struct {
	// Test allows the single host layouts that are only for tests: one quorum host / one mon, one replica (§6.3)
	Test bool `json:"test,omitempty"`
}

var (
	storageBackends     = map[string]StorageBackend{}
	storageBackendKinds []string
)

func registerStorageBackend(b StorageBackend) {
	if _, dup := storageBackends[b.Kind()]; dup {
		panic("storage backend registered twice: " + b.Kind())
	}
	storageBackends[b.Kind()] = b
	storageBackendKinds = append(storageBackendKinds, b.Kind())
	sort.Strings(storageBackendKinds)
}

// storageBackendOf returns the backend of a kind
func storageBackendOf(kind string) (StorageBackend, error) {
	if b := storageBackends[kind]; b != nil {
		return b, nil
	}
	return nil, planError("Unknown storage kind %q", kind)
}

// StorageBackendList returns every registered backend, ordered by kind
func StorageBackendList() []StorageBackend {
	list := []StorageBackend{}
	for _, kind := range storageBackendKinds {
		list = append(list, storageBackends[kind])
	}
	return list
}

// ListBackends returns the kinds of storage clusters, for system admins
func (a *StorageClusterAdmin) ListBackends(ctx context.Context) ([]StorageBackend, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	return StorageBackendList(), nil
}

// decodeStorageParams decodes the parameters of a kind into v, refusing keys the kind does not know: a misspelt
// parameter would otherwise be dropped without a word and the default used
func decodeStorageParams(kind string, raw json.RawMessage, v interface{}) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return planError("Invalid parameters for a %s cluster: %v", kind, err)
	}
	return nil
}

// storageDaemonRoles tells whether roles hold any role besides client
func storageDaemonRoles(roles []string) bool {
	for _, r := range roles {
		if r != model.StorageRoleClient {
			return true
		}
	}
	return false
}

// storageParamRange checks a numeric parameter; 0 means "not given" and leaves the default
func storageParamRange(kind, name string, value, min, max int) error {
	if value != 0 && (value < min || value > max) {
		return planError("Parameter %s of a %s cluster must be %d-%d", name, kind, min, max)
	}
	return nil
}

// storageLayoutChooser is a backend whose managed clusters come in layouts a parameter chooses (gpfs: replica or
// erasure code); a backend without it gives every cluster its DefaultLayout
type storageLayoutChooser interface {
	LayoutOf(params interface{}) string
}

// storageLayoutFor is the layout of a new managed cluster with the parsed parameters of its kind
func storageLayoutFor(backend StorageBackend, params interface{}) string {
	if c, ok := backend.(storageLayoutChooser); ok {
		if layout := c.LayoutOf(params); layout != "" {
			return layout
		}
	}
	return backend.DefaultLayout()
}

// storagePackageChecker is a backend that takes only some of its verified packages for some parameters (gpfs erasure
// code: the erasure code edition); the error says why the package does not fit
type storagePackageChecker interface {
	CheckPackage(params interface{}, pkg *model.StoragePackage) error
}

// storageCheckPackage: whether the package fits a new cluster with the parsed parameters of its kind
func storageCheckPackage(backend StorageBackend, params interface{}, pkg *model.StoragePackage) error {
	if c, ok := backend.(storagePackageChecker); ok {
		return c.CheckPackage(params, pkg)
	}
	return nil
}

// storageLayoutCapabilities is a backend whose clusters support less in some of their layouts (gpfs erasure code: no
// disk or host changes yet); nil keeps the capabilities of the kind
type storageLayoutCapabilities interface {
	LayoutCapabilities(layout string) *StorageCapabilities
}

// storageClusterCapabilities is what a cluster supports: what its kind does, narrowed by its layout
func storageClusterCapabilities(backend StorageBackend, cluster *model.StorageCluster) *StorageCapabilities {
	if lc, ok := backend.(storageLayoutCapabilities); ok && cluster != nil {
		if caps := lc.LayoutCapabilities(cluster.Layout); caps != nil {
			return caps
		}
	}
	return backend.Capabilities()
}

// storageLayoutDescriber is a backend whose clusters show what their layout is made of (gpfs erasure code: the code,
// the recovery group, the vdisk set); the keys are the backend's own
type storageLayoutDescriber interface {
	LayoutInfo(cluster *model.StorageCluster) map[string]interface{}
}

// StorageClusterLayoutInfo is what the layout of a cluster is made of, for the API; nil when its backend shows nothing
func StorageClusterLayoutInfo(cluster *model.StorageCluster) map[string]interface{} {
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return nil
	}
	if d, ok := backend.(storageLayoutDescriber); ok {
		return d.LayoutInfo(cluster)
	}
	return nil
}

// StorageClusterCapabilities is what a cluster supports, for the API; nil for a kind CloudLand does not know
func StorageClusterCapabilities(cluster *model.StorageCluster) *StorageCapabilities {
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return nil
	}
	return storageClusterCapabilities(backend, cluster)
}

// storageClusterMaintainer is a backend that has something to do for its ready clusters every minute, besides the
// tasks (the GPFS disks of a host that came back)
type storageClusterMaintainer interface {
	Maintain(ctx context.Context, cluster *model.StorageCluster, nodes []*model.StorageClusterNode)
}

// maintainStorageClusters gives every ready cluster its round: the health watchdog (§14.1), the hosts joining as
// clients on their own (§6.3), and the work of a kind that has some
func maintainStorageClusters(ctx context.Context) {
	db := dbs.DBContext(ctx)
	clusters := []*model.StorageCluster{}
	if err := db.Where("status IN ?", []string{model.StorageClusterReady, model.StorageClusterDegraded}).Find(&clusters).Error; err != nil {
		return
	}
	for _, c := range clusters {
		nodes := []*model.StorageClusterNode{}
		db.Where("cluster_id = ?", c.ID).Order("hostid").Find(&nodes)
		maintainStorageHealth(ctx, c, nodes)
		maintainAutoJoin(ctx, c)
		backend, err := storageBackendOf(c.Kind)
		if err != nil {
			continue
		}
		// Not while a structural task changes the cluster: the GPFS round would start the disk a replacement drops
		if m, ok := backend.(storageClusterMaintainer); ok && c.Status == model.StorageClusterReady && c.ActiveTask == 0 {
			m.Maintain(ctx, c, nodes)
		}
	}
}

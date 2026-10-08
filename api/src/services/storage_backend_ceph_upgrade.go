/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Upgrade of a managed Ceph cluster (shared-storage-design.md §8.7): the hosts install the release their
// distribution has now (ceph-common, cephadm, librbd), the cephadm hosts pull the image of that release, then
// cephadm upgrades the daemons one after the other (ceph orch upgrade). The daemons never run a newer release than the
// client libraries of the hosts (§8.1). No instance moves: QEMU keeps the librbd it started with until it is restarted
// or migrated, and Ceph serves older clients

import (
	"context"
	"encoding/json"
	"fmt"

	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageTaskKind("ceph:"+StorageTaskUpgrade, &storageTaskKind{Slot: storageSlotStructural, Finish: cephUpgradeFinish,
		Steps: map[string]*storageStepDef{
			"install": {Script: "ceph_install.sh", Input: cephUpgradeInstallInput, Done: cephUpgradeInstallDone},
			"upgrade": {Script: "ceph_cluster.sh", Input: cephUpgradeInput},
		}})
}

func (cephBackend) UpgradeParams(ctx context.Context, cluster *model.StorageCluster, req *StorageUpgrade) (*storageUpgradeParams, error) {
	if req.PackageUUID != "" || req.Finalize {
		return nil, planError("A Ceph cluster upgrades to the release of its distribution: it takes no package and has nothing to finalize")
	}
	if err := validStorageImage(req.Image); err != nil {
		return nil, err
	}
	// A cluster deployed from an image of its own (a private registry) can not have its next one guessed
	if p := cephParamsOf(cluster); p.Image != "" && req.Image == "" {
		return nil, planError("The cluster runs the image %s of its own: name the image of the new release", p.Image)
	}
	return &storageUpgradeParams{Image: req.Image}, nil
}

// cephUpgradeInstallInput: the install of a deployment, with the image of the upgrade (empty: the one of the release
// the host installs)
func cephUpgradeInstallInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	return cephInstallArgs(cluster, nodes, hostid, storageUpgradeOf(task).Image)
}

// cephUpgradeInstallDone: the daemon hosts installed one release and pulled one image, whose release (what the daemons
// run after the upgrade, maybe older than the hosts' with an image of its own) is not older than the one the cluster
// runs; the client only hosts may run another release, not older than the daemons will. The release of the image and
// the image go with the task for the upgrade step and the record at its end
func cephUpgradeInstallDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	cluster, nodes, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	daemonHost := map[int32]bool{}
	for _, n := range nodes {
		daemonHost[n.Hostid] = len(cephOrchLabels(n)) > 0
	}
	info := cephInfoOf(cluster)
	version, image, daemons := "", "", ""
	clients := map[int32]string{}
	for _, r := range runs {
		res := &cephInstalled{}
		_ = json.Unmarshal([]byte(r.Result), res)
		if res.Version != "" && !daemonHost[r.Hostid] {
			clients[r.Hostid] = res.Version
			continue
		}
		switch {
		case res.Version == "":
			return fmt.Errorf("%s reported no Ceph release", hostName(tx, r.Hostid))
		case version != "" && res.Version != version:
			return fmt.Errorf("%s installed Ceph %s, another daemon host %s: the daemon hosts must run one release", hostName(tx, r.Hostid), res.Version, version)
		case res.Image != "" && image != "" && res.Image != image:
			return fmt.Errorf("%s pulled image %s, another host %s", hostName(tx, r.Hostid), res.Image, image)
		case res.Image != "" && res.ImageVersion == "":
			return fmt.Errorf("%s reported no release of the image %s", hostName(tx, r.Hostid), res.Image)
		case res.ImageVersion != "" && daemons != "" && res.ImageVersion != daemons:
			return fmt.Errorf("%s found Ceph %s in the image, another host %s", hostName(tx, r.Hostid), res.ImageVersion, daemons)
		}
		version = res.Version
		if res.Image != "" {
			image, daemons = res.Image, res.ImageVersion
		}
	}
	if image == "" {
		return fmt.Errorf("no cephadm host pulled the image of the daemons")
	}
	if info.Version != "" && storageVersionLess(daemons, info.Version) {
		return fmt.Errorf("the image %s runs Ceph %s, older than the %s the cluster runs", image, daemons, info.Version)
	}
	if err := cephClientVersionCheck(tx, clients, daemons); err != nil {
		return err
	}
	p := storageUpgradeOf(task)
	p.Version, p.Image = daemons, image
	b, _ := json.Marshal(p)
	task.Params = string(b)
	return tx.Model(&model.StorageTask{}).Where("id = ?", task.ID).Update("params", task.Params).Error
}

func cephUpgradeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	p := storageUpgradeOf(task)
	if p.Version == "" || p.Image == "" {
		return nil, fmt.Errorf("the install step found no release to upgrade to")
	}
	return map[string]interface{}{"action": "upgrade", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "version": p.Version, "image": p.Image}, nil
}

// cephUpgradeFinish records the release and the image the daemons run now
func cephUpgradeFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	p := storageUpgradeOf(task)
	if !succeeded || p.Version == "" {
		return nil
	}
	cluster := &model.StorageCluster{}
	if err := tx.Take(cluster, task.ClusterID).Error; err != nil {
		return err
	}
	info := cephInfoOf(cluster)
	info.Version, info.Image = p.Version, p.Image
	return cephSaveInfo(tx, cluster, info, map[string]interface{}{"version": p.Version})
}

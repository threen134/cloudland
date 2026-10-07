/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A GPFS deployment in the erasure code layout and its deletion against PostgreSQL with the fake cland standing in for
// three hosts (shared-storage-design.md §7.9): the steps after the resolved disks, what each sends, how the disks get
// the names of their pdisks and what the deletion sends. The node script was run against mmvdisk in the validation VMs.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

func TestStorageGPFSECEDeployPG(t *testing.T) {
	f := newStcFixture(t)
	ctx := f.ctx
	viper.Set("vpn.secret_key", "storage-gpfs-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	client, err := minio.New("127.0.0.1:9", &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	must(t, err)
	oldClient, oldBucket := s3Client.Load(), s3Bucket
	s3Client.Store(client)
	s3Bucket = "images"
	defer func() { s3Client.Store(oldClient); s3Bucket = oldBucket }()

	h := stcHosts[:3]
	letters := "bcde"
	wwn := func(host int32, i int) string { return fmt.Sprintf("0x5000eee%d%d", host, i) }
	for _, host := range h {
		for i := 0; i < 4; i++ {
			// The WWN of a claimed disk comes from its stable id (wwn-0x...)
			must(t, f.db.Create(&model.HyperDisk{Hostid: host, DiskID: "wwn-" + wwn(host, i), Name: "sd" + string(letters[i]),
				Serial: fmt.Sprintf("SN%d%d", host, i), SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree,
				ScannedAt: time.Now()}).Error)
		}
	}
	defer f.db.Unscoped().Where("disk_id LIKE ?", "wwn-0x5000eee%").Delete(&model.HyperDisk{})
	debs := map[string]string{}
	for i, d := range []string{"gpfs.base_6.0.0-2_amd64.deb", "gpfs.gpl_6.0.0-2_all.deb", "gpfs.gskit_8.0.55-19.1_amd64.deb",
		"gpfs.msg.en-us_6.0.0-2_all.deb", "gpfs.license.ec_6.0.0-2_amd64.deb", "gpfs.docs_6.0.0-2_all.deb", "gpfs.gnr_6.0.0-2_amd64.deb",
		"gpfs.gnr.base_1.0.0-0_amd64.deb", "gpfs.gnr.support-scaleout_1.0.0-11_all.deb", "gpfs.adv_6.0.0-2_amd64.deb",
		"gpfs.crypto_6.0.0-2_amd64.deb", "gpfs.compression_6.0.0-2_amd64.deb", "gpfs.gui_6.0.0-2_all.deb"} {
		debs[d] = fmt.Sprintf("m%d", i)
	}
	manifest, _ := json.Marshal(debs)
	pkg := &model.StoragePackage{Kind: "gpfs", Version: "6.0.0.2", Edition: "erasure_code", FileName: "installer", SizeBytes: 1000,
		SHA256: strings.Repeat("b", 64), ObjectKey: "storage-packages/y/installer", PayloadLine: 680, Distros: `["ubuntu22","ubuntu24"]`,
		Manifest: string(manifest), Status: model.StoragePackageReady, AcceptedBy: "admin"}
	must(t, f.db.Create(pkg).Error)
	defer f.db.Unscoped().Delete(pkg)

	name := fmt.Sprintf("ece-pg-%d", time.Now().UnixNano()%1000000)
	disks := []*StorageDiskPlan{}
	for _, host := range h {
		for i := 0; i < 4; i++ {
			disks = append(disks, &StorageDiskPlan{Hostid: host, DiskID: "wwn-" + wwn(host, i)})
		}
	}
	req := &StorageClusterCreate{Name: name, PackageUUID: pkg.UUID, Plan: &StoragePlan{Kind: "gpfs",
		Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "quorum", "nsd"}}, {Hostid: h[1], Roles: []string{"quorum", "nsd"}},
			{Hostid: h[2], Roles: []string{"quorum", "nsd"}}},
		Disks: disks, Params: json.RawMessage(`{"layout":"ece","no_slot_map":true}`)}}
	short := *req
	short.Plan = &StoragePlan{Kind: "gpfs", Nodes: req.Plan.Nodes, Disks: disks[:9], Params: req.Plan.Params}
	_, _, err = StorageClusters.Create(ctx, &short)
	wantCode(t, err, ErrStorageInvalidPlan, "three disks on each server are fewer than 12")
	// The erasure code layout takes the erasure code edition only
	dm := *pkg
	dm.ID, dm.UUID, dm.Edition, dm.ObjectKey = 0, "", "data_management", "storage-packages/z/installer"
	must(t, f.db.Create(&dm).Error)
	defer f.db.Unscoped().Delete(&dm)
	other := *req
	other.PackageUUID = dm.UUID
	_, _, err = StorageClusters.Create(ctx, &other)
	wantCode(t, err, ErrStoragePackageState, "a data management edition package")

	cluster, task, err := StorageClusters.Create(ctx, req)
	must(t, err)
	defer f.db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	if cluster.Layout != model.StorageLayoutECE || task.Kind != "deploy" {
		t.Fatalf("cluster %+v task %+v", cluster, task)
	}
	_, nodes, _, err := StorageClusters.Get(ctx, cluster.UUID)
	must(t, err)
	for _, n := range nodes {
		if n.ReservedMemMB != 8192+2048 {
			t.Fatalf("a recovery group server reserves its pagepool and 2 GiB: %+v", n)
		}
	}
	caps := StorageClusterCapabilities(cluster)
	if caps == nil || caps.AddDisks || !caps.Pools {
		t.Fatalf("capabilities %+v", caps)
	}

	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }
	succeed(f.expect("precheck", "stc_precheck.sh", h...), func(s *stcSent) string {
		return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEY%d"}}`, s.hostid, s.hostid)
	})
	succeed(f.expect("join", "stc_join.sh", h...), none)
	succeed(f.expect("fetch", "stc_fetch.sh", h...), none)
	sent := f.expect("install", "gpfs_install.sh", h...)
	got, _ := sent[0].input["debs"].(map[string]interface{})
	if len(got) != 12 || got["gpfs.gnr_6.0.0-2_amd64.deb"] == nil || got["gpfs.gnr.support-scaleout_1.0.0-11_all.deb"] == nil ||
		got["gpfs.adv_6.0.0-2_amd64.deb"] == nil || got["gpfs.gui_6.0.0-2_all.deb"] != nil {
		t.Fatalf("install input %v", got)
	}
	succeed(sent, none)
	succeed(f.expect("build", "gpfs_build_gpl.sh", h...), none)
	succeed(f.expect("trust", "stc_ssh_trust.sh", h...), none)
	sent = f.expect("create cluster", "gpfs_cluster.sh", h[0])
	if sent[0].input["pagepool_mib"] != float64(1024) {
		t.Fatalf("the cluster default pagepool stays small, mmvdisk sets the servers': %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return fmt.Sprintf(`{"cluster_name":"%s.cloudland","cluster_id":"5678"}`, name) })
	succeed(f.expect("start", "gpfs_cluster.sh", h[0]), none)
	sent = f.expect("resolve", "stc_resolve_disks.sh", h...)
	if sent[0].input["nsddevices"] != false || len(sent[0].input["disks"].([]interface{})) != 4 {
		t.Fatalf("resolve input %v", sent[0].input)
	}
	// Kernel names in another order than the WWNs: the expression lists them sorted
	succeed(sent, func(s *stcSent) string {
		list := []string{}
		for i, d := range s.input["disks"].([]interface{}) {
			list = append(list, fmt.Sprintf(`{"id":%q,"path":"/dev/sd%c","name":"sd%c"}`, d.(map[string]interface{})["id"], letters[3-i], letters[3-i]))
		}
		return `{"disks":[` + strings.Join(list, ",") + `]}`
	})
	nc, rg, vs := gpfsECENames(cluster)
	expr := "10.93.0.1:sdb,sdc,sdd,sde;10.93.0.2:sdb,sdc,sdd,sde;10.93.0.3:sdb,sdc,sdd,sde"
	sent = f.expect("ece configure", "gpfs_ece.sh", h[0])
	in := sent[0].input
	if in["action"] != "configure" || in["node_class"] != nc || in["disk_expr"] != expr || in["pagepool_bytes"] != float64(8<<30) ||
		len(in["servers"].([]interface{})) != 3 {
		t.Fatalf("configure input %v", in)
	}
	succeed(sent, func(*stcSent) string { return `{"topology":"ECE 4 HDD"}` })
	sent = f.expect("ece slots", "gpfs_ece.sh", h[0])
	if sent[0].input["action"] != "slots" || sent[0].input["no_slot_map"] != true {
		t.Fatalf("slots input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("ece create rg", "gpfs_ece.sh", h[0])
	if sent[0].input["recovery_group"] != rg || sent[0].input["disk_expr"] != expr {
		t.Fatalf("create_rg input %v", sent[0].input)
	}
	// The pdisks as mmlspdisk reports them: by WWN; one of them by its kernel name only
	succeed(sent, func(*stcSent) string {
		list := []string{}
		for k, host := range h {
			for i := 0; i < 4; i++ {
				w := strings.ToUpper(strings.TrimPrefix(wwn(host, i), "0x"))
				if k == 2 && i == 3 {
					w = ""
				}
				list = append(list, fmt.Sprintf(`{"name":"n%03dp%03d","ip":"10.93.0.%d","device":"sd%c","wwn":"%s","state":"ok"}`,
					k+1, i+1, k+1, letters[3-i], map[bool]string{true: "", false: "naa." + w}[w == ""]))
			}
		}
		return `{"pdisks":[` + strings.Join(list, ",") + `]}`
	})
	sent = f.expect("ece create vs", "gpfs_ece.sh", h[0])
	if sent[0].input["vdisk_set"] != vs || sent[0].input["code"] != "4+2p" || sent[0].input["block_size"] != "4M" || sent[0].input["set_size_pct"] != float64(80) {
		t.Fatalf("create_vs input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("ece create fs", "gpfs_ece.sh", h[0])
	if sent[0].input["fs_name"] != "fs1" || sent[0].input["mount_point"] != "/gpfs/fs1" || sent[0].input["vdisk_set"] != vs {
		t.Fatalf("create_fs input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return `{"capacity_bytes":9000,"free_bytes":8000}` })
	succeed(f.expect("finish", "stc_finish.sh", h...), none)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	c := &model.StorageCluster{}
	must(t, f.db.Take(c, cluster.ID).Error)
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(c.Attrs), &attrs)
	if c.Status != model.StorageClusterReady || attrs["recovery_group"] != rg || attrs["vdisk_set"] != vs || attrs["ece_code"] != "4+2p" {
		t.Fatalf("deployed cluster %+v attrs %v", c, attrs)
	}
	fs := &model.StorageFilesystem{}
	must(t, f.db.Where("cluster_id = ?", c.ID).Take(fs).Error)
	if fs.DataReplicas != 1 || fs.CapacityBytes != 9000 || fs.BlockSize != "4M" {
		t.Fatalf("file system %+v", fs)
	}
	_, _, cdisks, _ := StorageClusters.Get(ctx, cluster.UUID)
	names := map[string]bool{}
	for _, d := range cdisks {
		var k, i int
		if _, err := fmt.Sscanf(d.Name, "n%03dp%03d", &k, &i); err != nil || d.Status != model.StorageDiskActive || d.FsID != fs.ID {
			t.Fatalf("deployed disk %+v", d)
		}
		// pdisk p<i> was reported on the kernel name sd<letters[4-i]>, which is the disk of WWN index 4-i
		if d.WWN != wwn(h[k-1], i-1) {
			t.Fatalf("disk %s (%s) named %s", d.DiskID, d.WWN, d.Name)
		}
		names[d.Name] = true
	}
	if len(names) != 12 {
		t.Fatalf("12 disks, 12 pdisk names: %v", names)
	}
	_, _, _, _, err = storageClusterForChange(ctx, cluster.UUID, func(c *StorageCapabilities) bool { return c.AddDisks })
	if err == nil || !strings.Contains(err.Error(), "do not support") {
		t.Fatalf("adding disks to a recovery group comes later: %v", err)
	}

	del, err := StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	sent = f.expect("teardown", "gpfs_cluster.sh", h[0])
	ece, _ := sent[0].input["ece"].(map[string]interface{})
	if len(sent[0].input["nsds"].([]interface{})) != 0 || ece["recovery_group"] != rg || ece["node_class"] != nc ||
		ece["vdisk_sets"] != nil || sent[0].input["filesystems"].([]interface{})[0] != "fs1" {
		t.Fatalf("teardown input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("leave", "stc_leave.sh", h...)
	if len(sent[0].input["wipe"].([]interface{})) != 4 {
		t.Fatalf("leave input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
}

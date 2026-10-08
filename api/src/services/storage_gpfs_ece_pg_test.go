/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A GPFS deployment in the erasure code layout, its changes and its deletion against PostgreSQL with the fake cland
// standing in for the hosts (shared-storage-design.md §7.9): the steps after the resolved disks, what each sends, how
// the disks get the names of their pdisks; a failed pdisk replaced, a server added, a disk added on every server, the
// server removed again; what the upgrade and the deletion send. The node script was run against mmvdisk in the
// validation VMs (the deployment); the changes only against stubs (WSL stc-test15.sh).

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
	// The fourth host joins later as a server; one more disk on every server for the resize, one on the second for the
	// replacement
	for i := 0; i < 4; i++ {
		must(t, f.db.Create(&model.HyperDisk{Hostid: stcHosts[3], DiskID: "wwn-" + wwn(stcHosts[3], i), Name: "sd" + string(letters[i]),
			Serial: fmt.Sprintf("SN%d%d", stcHosts[3], i), SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	for _, host := range stcHosts {
		must(t, f.db.Create(&model.HyperDisk{Hostid: host, DiskID: "wwn-" + wwn(host, 5), Name: "sdf", Serial: fmt.Sprintf("SN%d5", host),
			SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	must(t, f.db.Create(&model.HyperDisk{Hostid: h[1], DiskID: "wwn-" + wwn(h[1], 8), Name: "sdj", Serial: "SNR", SizeBytes: 2 << 40,
		Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
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
	if caps == nil || !caps.AddDisks || !caps.AddNodes || !caps.ReplaceDisk || caps.RemoveDisk || !caps.Pools {
		t.Fatalf("capabilities %+v", caps)
	}

	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }
	succeed(f.expect("precheck", "stc_precheck.sh", h...), func(s *stcSent) string {
		return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEY%d","mem_mib":32000}}`, s.hostid, s.hostid)
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
	sets, _ := sent[0].input["sets"].([]interface{})
	if len(sets) != 1 {
		t.Fatalf("create_vs input %v", sent[0].input)
	}
	set := sets[0].(map[string]interface{})
	if set["vdisk_set"] != vs || set["code"] != "4+2p" || set["block_size"] != "4M" || set["set_size_pct"] != float64(80) || set["da_type"] != nil {
		t.Fatalf("create_vs input: one data set on the only array %v", set)
	}
	succeed(sent, none)
	sent = f.expect("ece create fs", "gpfs_ece.sh", h[0])
	if sent[0].input["fs_name"] != "fs1" || sent[0].input["mount_point"] != "/gpfs/fs1" || fmt.Sprint(sent[0].input["vdisk_sets"]) != "["+vs+"]" ||
		sent[0].input["data_pool"] != "" {
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
	var server *model.StorageClusterNode
	_, nodes, _, _ = StorageClusters.Get(ctx, cluster.UUID)
	for _, n := range nodes {
		if n.Hostid == h[1] {
			server = n
		}
	}
	if !strings.Contains(server.Attrs, `"mem_mib":32000`) {
		t.Fatalf("the memory a server reported is kept with it: %s", server.Attrs)
	}
	if !strings.Contains(c.Attrs, `"vdisk_sets":["`+vs+`"]`) {
		t.Fatalf("the vdisk sets are recorded: %s", c.Attrs)
	}

	// The resolve step of a change: every disk asked for by the kernel name of its scan
	resolveByScan := func(s *stcSent) string {
		list := []string{}
		for _, d := range s.input["disks"].([]interface{}) {
			id := d.(map[string]interface{})["id"].(string)
			hd := &model.HyperDisk{}
			must(t, f.db.Where("hostid = ? AND disk_id = ?", s.hostid, id).Take(hd).Error)
			list = append(list, fmt.Sprintf(`{"id":%q,"path":"/dev/%s","name":"%s"}`, id, hd.Name, hd.Name))
		}
		return `{"disks":[` + strings.Join(list, ",") + `]}`
	}
	naa := func(w string) string { return "naa." + strings.ToUpper(strings.TrimPrefix(w, "0x")) }
	pdisk := func(name string, host int32, dev, w string) string {
		return fmt.Sprintf(`{"name":"%s","ip":"10.93.0.%d","device":"%s","wwn":"%s","state":"ok"}`, name, host-stcHosts[0]+1, dev, naa(w))
	}
	diskNamed := func(name string) *model.StorageClusterDisk {
		_, _, list, _ := StorageClusters.Get(ctx, cluster.UUID)
		for _, d := range list {
			if d.Name == name {
				return d
			}
		}
		return nil
	}

	// ---- a failed pdisk replaced by a new disk of its server: the new disk takes the name ----
	failed := diskNamed("n002p002")
	must(t, f.db.Model(&model.StorageClusterDisk{}).Where("id = ?", failed.ID).Updates(map[string]interface{}{"state": "missing", "checked_at": time.Now()}).Error)
	newWWN := wwn(h[1], 8)
	rep, err := StorageClusters.ReplaceDisk(ctx, cluster.UUID, failed.UUID, &StorageDiskPlan{DiskID: "wwn-" + newWWN})
	must(t, err)
	sent = f.expect("replace resolve", "stc_resolve_disks.sh", h[1])
	inUse := 0
	for _, d := range sent[0].input["disks"].([]interface{}) {
		if d.(map[string]interface{})["in_use"] == true {
			inUse++
		}
	}
	if len(sent[0].input["disks"].([]interface{})) != 4 || inUse != 3 {
		t.Fatalf("the server resolves its disks in use (identity only) and the new one: %v", sent[0].input)
	}
	succeed(sent, resolveByScan)
	sent = f.expect("ece replace", "gpfs_ece.sh", h[0])
	in = sent[0].input
	if in["action"] != "replace" || in["pdisk"] != "n002p002" || in["device"] != "sdj" || in["ip"] != "10.93.0.2" || in["wwn"] != newWWN ||
		in["slot_map"] != false || in["recovery_group"] != rg {
		t.Fatalf("replace input %v", in)
	}
	// The old pdisk drains under a temporary name: never taken for the new disk
	succeed(sent, func(*stcSent) string {
		return `{"pdisks":[` + pdisk("n002p002#0010", h[1], "sdc", failed.WWN) + "," + pdisk("n002p002", h[1], "sdj", newWWN) + `]}`
	})
	sent = f.expect("replace release", "stc_release_disks.sh", h[1])
	if sent[0].input["no_wipe"] != true {
		t.Fatalf("the failed disk is given back unwiped: %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(rep.ID, model.StorageTaskSucceeded, "")
	if d := diskNamed("n002p002"); d == nil || d.WWN != newWWN || d.Status != model.StorageDiskActive || d.FsID != fs.ID {
		t.Fatalf("the new disk under the old pdisk name: %+v", d)
	}
	var left int64
	f.db.Model(&model.StorageClusterDisk{}).Where("id = ?", failed.ID).Count(&left)
	if left != 0 {
		t.Fatal("the failed disk's claim goes")
	}

	// ---- a fourth server: every server resolves its disks for the disk list, the group takes it ----
	h4 := stcHosts[3]
	addDisks := []*StorageDiskPlan{}
	for i := 0; i < 4; i++ {
		addDisks = append(addDisks, &StorageDiskPlan{Hostid: h4, DiskID: "wwn-" + wwn(h4, i)})
	}
	add, err := StorageClusters.AddNodes(ctx, cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: h4, Roles: []string{"nsd"}}}, Disks: addDisks})
	must(t, err)
	sent = f.expect("add precheck", "stc_precheck.sh", h4)
	if r, _ := sent[0].input["readiness"].(map[string]interface{}); r["min_mem_mib"] != float64(10240) {
		t.Fatalf("a server joining is checked for its readiness: %v", sent[0].input["readiness"])
	}
	succeed(sent, func(s *stcSent) string {
		return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEY%d","mem_mib":33000}}`, s.hostid, s.hostid)
	})
	succeed(f.expect("add join", "stc_join.sh", h4), none)
	succeed(f.expect("add fetch", "stc_fetch.sh", h4), none)
	succeed(f.expect("add install", "gpfs_install.sh", h4), none)
	succeed(f.expect("add build", "gpfs_build_gpl.sh", h4), none)
	succeed(f.expect("add trust", "stc_ssh_trust.sh", stcHosts...), none)
	succeed(f.expect("add node", "gpfs_cluster.sh", h[0]), none)
	succeed(f.expect("add resolve", "stc_resolve_disks.sh", stcHosts...), resolveByScan)
	sent = f.expect("ece add servers", "gpfs_ece.sh", h[0])
	in = sent[0].input
	full, _ := in["full_expr"].(string)
	if in["action"] != "add_servers" || fmt.Sprint(in["servers"]) != "[10.93.0.4]" || in["disk_expr"] != "10.93.0.4:sdb,sdc,sdd,sde" ||
		len(strings.Split(full, ";")) != 4 || !strings.Contains(full, "10.93.0.2:sdb,sdd,sde,sdj") || in["total_servers"] != float64(4) ||
		in["node_class"] != nc || in["recovery_group"] != rg || in["no_slot_map"] != true {
		t.Fatalf("add_servers input %v", in)
	}
	succeed(sent, func(*stcSent) string {
		list := []string{}
		for i := 0; i < 4; i++ {
			list = append(list, pdisk(fmt.Sprintf("n004p%03d", i+1), h4, "sd"+string(letters[i]), wwn(h4, i)))
		}
		return `{"pdisks":[` + strings.Join(list, ",") + `]}`
	})
	succeed(f.expect("add finish", "stc_finish.sh", h4), none)
	f.wantTask(add.ID, model.StorageTaskSucceeded, "")
	for i := 0; i < 4; i++ {
		if d := diskNamed(fmt.Sprintf("n004p%03d", i+1)); d == nil || d.Status != model.StorageDiskActive || d.FsID != fs.ID || d.Hostid != h4 {
			t.Fatalf("a disk of the new server: %+v", d)
		}
	}

	// ---- a disk more on every server: the group resizes, a new vdisk set takes the capacity ----
	more := []*StorageDiskPlan{}
	for _, host := range stcHosts {
		more = append(more, &StorageDiskPlan{Hostid: host, DiskID: "wwn-" + wwn(host, 5)})
	}
	_, err = StorageClusters.AddDisks(ctx, cluster.UUID, &StorageClusterExpand{Disks: more[:3]})
	wantCode(t, err, ErrStorageInvalidPlan, "a disk on 3 of the 4 servers")
	grow, err := StorageClusters.AddDisks(ctx, cluster.UUID, &StorageClusterExpand{Disks: more})
	must(t, err)
	succeed(f.expect("resize resolve", "stc_resolve_disks.sh", stcHosts...), resolveByScan)
	sent = f.expect("ece resize", "gpfs_ece.sh", h[0])
	in = sent[0].input
	if in["action"] != "resize" || in["vdisk_set"] != fmt.Sprintf("cl%d_vs2", cluster.ID) || in["fs_name"] != "fs1" || in["code"] != "4+2p" ||
		in["set_size_pct"] != float64(80) || in["da_type"] != nil || !strings.Contains(in["disk_expr"].(string), "10.93.0.4:sdb,sdc,sdd,sde,sdf") {
		t.Fatalf("resize input %v", in)
	}
	succeed(sent, func(*stcSent) string {
		list := []string{}
		for k, host := range stcHosts {
			list = append(list, pdisk(fmt.Sprintf("n%03dp005", k+1), host, "sdf", wwn(host, 5)))
		}
		return `{"pdisks":[` + strings.Join(list, ",") + `]}`
	})
	f.wantTask(grow.ID, model.StorageTaskSucceeded, "")
	if d := diskNamed("n003p005"); d == nil || d.Status != model.StorageDiskActive {
		t.Fatalf("a new disk of the resize: %+v", d)
	}
	must(t, f.db.Take(c, cluster.ID).Error)
	if !strings.Contains(c.Attrs, fmt.Sprintf(`"vdisk_sets":["%s","cl%d_vs2"]`, vs, cluster.ID)) {
		t.Fatalf("the new vdisk set is recorded: %s", c.Attrs)
	}

	// ---- the fourth server leaves: the file system and the group give up its share first ----
	rm, err := StorageClusters.RemoveNode(ctx, cluster.UUID, &StorageNodeRemove{Hostid: h4})
	must(t, err)
	sent = f.expect("ece remove server", "gpfs_ece.sh", h[0])
	if sent[0].input["action"] != "remove_server" || sent[0].input["ip"] != "10.93.0.4" || sent[0].input["recovery_group"] != rg {
		t.Fatalf("remove_server input %v", sent[0].input)
	}
	succeed(sent, none)
	succeed(f.expect("remove node", "gpfs_cluster.sh", h[0]), none)
	succeed(f.expect("remove leave", "stc_leave.sh", h4), none)
	f.wantTask(rm.ID, model.StorageTaskSucceeded, "")
	var h4disks int64
	f.db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, h4).Count(&h4disks)
	if h4disks != 0 {
		t.Fatal("the disks of the server that left go with it")
	}
	_, err = StorageClusters.RemoveNode(ctx, cluster.UUID, &StorageNodeRemove{Hostid: h[2]})
	wantCode(t, err, ErrStorageInvalidPlan, "a recovery group needs 3 servers")

	// ---- the upgrade suspends a server in the group; finalize raises the group's version ----
	up, err := gpfsUpgradeInput(ctx, f.db, &model.StorageTask{ClusterID: cluster.ID}, nil, h[0])
	must(t, err)
	if e, _ := up.(map[string]interface{})["ece"].(map[string]interface{}); e["recovery_group"] != rg {
		t.Fatalf("upgrade input of a server %v", up)
	}
	fin, err := gpfsFinalizeInput(ctx, f.db, &model.StorageTask{ClusterID: cluster.ID}, nil, h[0])
	must(t, err)
	if fin.(map[string]interface{})["recovery_group"] != rg {
		t.Fatalf("finalize input %v", fin)
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
	if len(sent[0].input["wipe"].([]interface{})) != 5 {
		t.Fatalf("leave input: the 4 disks of the deployment and the one of the resize %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
}

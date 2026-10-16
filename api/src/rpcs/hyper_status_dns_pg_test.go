/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// Needs PostgreSQL: CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run PG; skipped without it

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"gorm.io/gorm"

	"api/src/model"
	"api/src/services"
)

// dnsTestHosts points hyper-hosts at a temporary file and removes the given hosts, with their resource rows, before
// and after the test
func dnsTestHosts(t *testing.T, db *gorm.DB, content string, hostids ...int32) string {
	t.Helper()
	hostsFile := filepath.Join(t.TempDir(), "hyper-hosts")
	t.Setenv("HYPER_DNS_HOSTS_FILE", hostsFile)
	if err := os.WriteFile(hostsFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		db.Unscoped().Where("hostid IN ?", hostids).Delete(&model.Hyper{})
		db.Unscoped().Where("hostid IN ?", hostids).Delete(&model.Resource{})
	}
	cleanup()
	t.Cleanup(cleanup)
	return hostsFile
}

func dnsTestHyper(t *testing.T, db *gorm.DB, hostid int32, name, ip string) {
	t.Helper()
	// A route IP is set so the heartbeat does not try to give the host a system router address
	if err := db.Create(&model.Hyper{Hostid: hostid, Hostname: name, HostIP: ip, Status: 1, RouteIP: "198.51.100.10"}).Error; err != nil {
		t.Fatal(err)
	}
}

// hostsLines gives the lines of a hosts file, sorted, and fails when a temporary file was left beside it
func hostsLines(t *testing.T, hostsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(hostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(hostsFile)); len(entries) != 1 {
		t.Fatalf("only %s should be in its directory, found %d files", filepath.Base(hostsFile), len(entries))
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	sort.Strings(lines)
	return lines
}

func expectHosts(t *testing.T, hostsFile, step string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := hostsLines(t, hostsFile); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("%s: hyper-hosts is %q, want %q", step, got, want)
	}
}

// The heartbeat keeps hyper-hosts in step with the name and address a host reports (internal-dns-plan.md §4.2)
func TestHyperStatusDnsPG(t *testing.T) {
	db := consoleTestDB(t)
	const node, twin = int32(9854), int32(9855)
	hostsFile := dnsTestHosts(t, db, "10.98.1.9 other-node\n10.98.1.3 dns-node\n", node, twin)
	dnsTestHyper(t, db, node, "dns-node", "10.98.1.3")
	report := func(name, ip, cpuModel string) string {
		return fmt.Sprintf("hyper_status.sh '%d' '%s' '4' '8' '1000' '2000' '10' '20' '1' '%s' 'z' '1' '1' '1' '%s'", node, name, ip, cpuModel)
	}
	hostIP := func() string {
		got := &model.Hyper{}
		if err := db.Where("hostid = ?", node).Take(got).Error; err != nil {
			t.Fatal(err)
		}
		return got.HostIP
	}

	nodeCallback(t, node, report("dns-node", "10.98.1.3", "cpu"))
	expectHosts(t, hostsFile, "nothing changed", "10.98.1.9 other-node", "10.98.1.3 dns-node")

	// The file is rewritten from committed rows: it only shows the new address when the write comes after the commit
	nodeCallback(t, node, report("dns-node", "10.98.1.4", "cpu"))
	expectHosts(t, hostsFile, "new address", "10.98.1.9 other-node", "10.98.1.4 dns-node")

	// No address for a moment (booting, bond switch-over): the known one stays, in the table and in the file
	nodeCallback(t, node, report("dns-node", "", "cpu"))
	if ip := hostIP(); ip != "10.98.1.4" {
		t.Fatalf("an empty address replaced the known one: host_ip %q", ip)
	}
	expectHosts(t, hostsFile, "empty address", "10.98.1.9 other-node", "10.98.1.4 dns-node")

	// An empty cpu model is the trailing argument the decoding drops: the report goes through all the same
	nodeCallback(t, node, report("dns-node", "10.98.1.4", ""))

	// Renamed while another host still has the old name: the old name now points at that host instead of going away
	dnsTestHyper(t, db, twin, "dns-node", "10.98.1.7")
	nodeCallback(t, node, report("dns-node-b", "10.98.1.4", "cpu"))
	expectHosts(t, hostsFile, "renamed beside a twin", "10.98.1.9 other-node", "10.98.1.7 dns-node", "10.98.1.4 dns-node-b")

	// The last host with a name is gone (HyperAdmin.Delete syncs its name after deleting the row)
	db.Unscoped().Where("hostid = ?", twin).Delete(&model.Hyper{})
	services.SyncHostsInDns("dns-node")
	expectHosts(t, hostsFile, "deleted", "10.98.1.9 other-node", "10.98.1.4 dns-node-b")
}

// The clapi start rebuilds hyper-hosts with the service names and every host in one write (internal-dns-plan.md §4.1)
func TestRebuildDnsHostsFilePG(t *testing.T) {
	db := consoleTestDB(t)
	const first, second = int32(9856), int32(9857)
	hostsFile := dnsTestHosts(t, db, "10.98.2.99 leftover\n", first, second)
	dnsTestHyper(t, db, first, "dns-rebuild", "10.98.2.1")
	dnsTestHyper(t, db, second, "dns-rebuild", "10.98.2.2")
	keys := []string{"management_vip", "s3.endpoint", "minio.hostname", "clapi.hostname"}
	saved := map[string]string{}
	for _, k := range keys {
		saved[k] = viper.GetString(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			viper.Set(k, saved[k])
		}
	})
	viper.Set("management_vip", "10.98.2.100")
	viper.Set("clapi.hostname", "clapi.cloudland.internal")

	check := func(step, endpoint, minio string, want ...string) {
		t.Helper()
		viper.Set("s3.endpoint", endpoint)
		viper.Set("minio.hostname", minio)
		services.RebuildDnsHostsFile()
		lines := hostsLines(t, hostsFile)
		names := map[string]string{}
		for _, l := range lines {
			f := strings.Fields(l)
			if prev, dup := names[f[1]]; dup {
				t.Fatalf("%s: %s is in the file twice (%s, %s)", step, f[1], prev, f[0])
			}
			names[f[1]] = f[0]
		}
		if _, ok := names["leftover"]; ok {
			t.Fatalf("%s: the rebuild kept a line of the old file", step)
		}
		if names["dns-rebuild"] != "10.98.2.1" {
			t.Fatalf("%s: a name two hosts share should go to the lower host id, got %q", step, names["dns-rebuild"])
		}
		for _, w := range want {
			f := strings.Fields(w)
			if names[f[1]] != f[0] {
				t.Fatalf("%s: want %q, file has %q for %s", step, w, names[f[1]], f[1])
			}
		}
		for _, n := range []string{"files.corp.lan", "images.cloudland.internal", "s3.amazonaws.com"} {
			if _, ok := names[n]; ok && !hostsHasName(want, n) {
				t.Fatalf("%s: %s should not be in the file", step, n)
			}
		}
	}
	check("built-in MinIO with its own name", "files.corp.lan:9000", "files.corp.lan",
		"10.98.2.100 files.corp.lan", "10.98.2.100 clapi.cloudland.internal")
	check("default internal name", "http://images.cloudland.internal:9000", "images.cloudland.internal",
		"10.98.2.100 images.cloudland.internal", "10.98.2.100 clapi.cloudland.internal")
	check("outside S3", "s3.amazonaws.com", "images.cloudland.internal", "10.98.2.100 clapi.cloudland.internal")
}

func hostsHasName(lines []string, name string) bool {
	for _, l := range lines {
		if f := strings.Fields(l); len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

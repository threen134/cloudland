package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/viper"

	"api/src/model"

	. "api/src/common"
)

// dnsHostsMu guards hyper-hosts: SyncHostsInDns and RebuildDnsHostsFile both read and rewrite it under this lock
var dnsHostsMu sync.Mutex

func dnsHostsFile() string {
	if v := os.Getenv("HYPER_DNS_HOSTS_FILE"); v != "" {
		return v
	}
	return "/opt/cloudland/dns/hosts/hyper-hosts"
}

// hostsLineMatchesHostname 精确匹配 hosts 行的 hostname 字段（防止子串误匹配）。
// 注释行（# 开头）直接返回 false，不参与匹配。
func hostsLineMatchesHostname(line, hostname string) bool {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return false
	}
	fields := strings.Fields(line)
	return len(fields) >= 2 && fields[1] == hostname
}

// hostsEntry is one "ip name" line of hyper-hosts
type hostsEntry struct {
	ip, name string
}

// hyperDnsEntries gives the address of each host name in the hypers table, of the given names only when there are
// some. A name several hosts share goes to the one with the lowest host id, so every writer picks the same one
func hyperDnsEntries(names []string) ([]hostsEntry, error) {
	query := DB().Model(&model.Hyper{}).Select("hostname, host_ip").Where("host_ip <> '' AND hostname <> ''")
	if len(names) > 0 {
		query = query.Where("hostname IN ?", names)
	}
	var hypers []model.Hyper
	if err := query.Order("hostid").Find(&hypers).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var entries []hostsEntry
	for _, h := range hypers {
		if !seen[h.Hostname] {
			seen[h.Hostname] = true
			entries = append(entries, hostsEntry{h.HostIP, h.Hostname})
		}
	}
	return entries, nil
}

// serviceDnsEntries are the control plane names compute nodes reach, all at MANAGEMENT_VIP: the S3 endpoint when it
// is the built-in MinIO (MINIO_HOSTNAME, or any .cloudland.internal name; an outside S3 resolves upstream) and
// CLAPI_HOSTNAME, which capture uploads go to
func serviceDnsEntries() []hostsEntry {
	vip := viper.GetString("management_vip")
	if vip == "" {
		logger.Info("DNS: management_vip not set, skip S3/clapi hostname registration")
		return nil
	}
	var entries []hostsEntry
	if s3Host := ParseS3Host(); s3Host != "" &&
		(strings.HasSuffix(s3Host, ".cloudland.internal") || strings.EqualFold(s3Host, viper.GetString("minio.hostname"))) {
		entries = append(entries, hostsEntry{vip, s3Host})
	}
	if clapiHost := viper.GetString("clapi.hostname"); clapiHost != "" {
		entries = append(entries, hostsEntry{vip, clapiHost})
	}
	return entries
}

// writeHostsFile replaces a hosts file with the given lines through a rename. The temporary file starts with a dot:
// dnsmasq skips dotfiles in --hostsdir, while a plain "hyper-hosts.tmp" would be read as one more hosts file whose
// entries stay after the rename. The mode is set explicitly since dnsmasq reads the file as an unprivileged user
func writeHostsFile(hostsFile string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	tmpFile := filepath.Join(filepath.Dir(hostsFile), "."+filepath.Base(hostsFile)+".tmp")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Chmod(tmpFile, 0644); err != nil {
		os.Remove(tmpFile)
		return err
	}
	if err := os.Rename(tmpFile, hostsFile); err != nil {
		os.Remove(tmpFile)
		return err
	}
	return nil
}

// SyncHostsInDns rewrites the hyper-hosts lines of the given host names from the hypers table, in a single write: a
// name no host has any more goes away, otherwise it points at the address of the host that has it (the lowest host id
// when several share it). It reads committed rows, so it is called once the change is committed. Other lines, the
// control plane service names among them, stay as they are
func SyncHostsInDns(names ...string) {
	var wanted []string
	for _, n := range names {
		if n != "" {
			wanted = append(wanted, n)
		}
	}
	if len(wanted) == 0 {
		return
	}
	dnsHostsMu.Lock()
	defer dnsHostsMu.Unlock()
	entries, err := hyperDnsEntries(wanted)
	if err != nil {
		logger.Errorf("DNS: query of %v failed: %v", wanted, err)
		return
	}
	hostsFile := dnsHostsFile()
	data, err := os.ReadFile(hostsFile)
	if err != nil && !os.IsNotExist(err) {
		logger.Errorf("DNS: read %s failed: %v", hostsFile, err)
		return
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		stale := false
		for _, n := range wanted {
			if hostsLineMatchesHostname(l, n) {
				stale = true
				break
			}
		}
		if !stale {
			lines = append(lines, l)
		}
	}
	for _, e := range entries {
		lines = append(lines, e.ip+" "+e.name)
	}
	if strings.Join(lines, "\n")+"\n" == string(data) {
		return
	}
	if err := writeHostsFile(hostsFile, lines); err != nil {
		logger.Errorf("DNS: rewrite of %s failed: %v", hostsFile, err)
		return
	}
	logger.Infof("DNS: synced %v (%d found)", wanted, len(entries))
}

// RebuildDnsHostsFile rewrites hyper-hosts at clapi start from the configuration and the hypers table: the control
// plane service names and every host, in one write so that none of them is missing in between
func RebuildDnsHostsFile() {
	dnsHostsMu.Lock()
	defer dnsHostsMu.Unlock()
	hypers, err := hyperDnsEntries(nil)
	if err != nil {
		logger.Errorf("DNS: rebuild query failed: %v", err)
		return
	}

	hostsFile := dnsHostsFile()
	if err := os.MkdirAll(filepath.Dir(hostsFile), 0755); err != nil {
		logger.Errorf("DNS: mkdir failed: %v", err)
		return
	}

	seen := map[string]bool{}
	var lines []string
	for _, e := range append(serviceDnsEntries(), hypers...) {
		if !seen[e.name] {
			seen[e.name] = true
			lines = append(lines, e.ip+" "+e.name)
		}
	}
	if err := writeHostsFile(hostsFile, lines); err != nil {
		logger.Errorf("DNS: rebuild of %s failed: %v", hostsFile, err)
		return
	}
	logger.Infof("DNS: rebuilt %s with %d entries", hostsFile, len(lines))
}

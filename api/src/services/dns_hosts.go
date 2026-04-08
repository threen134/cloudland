package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"api/src/model"

	. "api/src/common"
)

// dnsHostsMu 保护 hyper-hosts 文件的并发读写。
// RegisterHostInDns、RemoveHostFromDns、RebuildDnsHostsFile 均在此锁下操作。
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

// RegisterHostInDns 将 hostname→ip 写入 hyper-hosts（幂等，支持 IP 变更）。
// rename 原子替换后 dnsmasq inotify 自动重载，无需 SIGHUP。
// 注意：在 HyperAdmin.Deploy 中于节点 DEPLOYING 状态即写入；若部署失败，
// DNS 条目在后续 Delete 时由 RemoveHostFromDns 清理。
func RegisterHostInDns(hostname, ip string) {
	if hostname == "" || ip == "" {
		return
	}
	dnsHostsMu.Lock()
	defer dnsHostsMu.Unlock()
	hostsFile := dnsHostsFile()

	var lines []string
	if data, err := os.ReadFile(hostsFile); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if hostsLineMatchesHostname(l, hostname) {
				continue // 过滤旧条目（支持 IP 变更）
			}
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	lines = append(lines, ip+" "+hostname)

	content := strings.Join(lines, "\n") + "\n"
	tmpFile := filepath.Join(filepath.Dir(hostsFile), "."+filepath.Base(hostsFile)+".tmp")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		logger.Errorf("DNS: write %s failed: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, hostsFile); err != nil {
		logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, hostsFile, err)
		return
	}
	logger.Infof("DNS: registered %s -> %s", hostname, ip)
}

// RemoveHostFromDns 从 hyper-hosts 删除指定 hostname 条目。
func RemoveHostFromDns(hostname string) {
	if hostname == "" {
		return
	}
	dnsHostsMu.Lock()
	defer dnsHostsMu.Unlock()
	hostsFile := dnsHostsFile()

	data, err := os.ReadFile(hostsFile)
	if err != nil {
		return
	}
	var lines []string
	found := false
	for _, l := range strings.Split(string(data), "\n") {
		if hostsLineMatchesHostname(l, hostname) {
			found = true
			continue
		}
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if !found {
		return
	}

	content := strings.Join(lines, "\n") + "\n"
	tmpFile := filepath.Join(filepath.Dir(hostsFile), "."+filepath.Base(hostsFile)+".tmp")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		logger.Errorf("DNS: write %s failed: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, hostsFile); err != nil {
		logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, hostsFile, err)
		return
	}
	logger.Infof("DNS: removed %s", hostname)
}

// RebuildDnsHostsFile 从数据库 hypers 表重建 hyper-hosts，供 clapi 启动时调用。
func RebuildDnsHostsFile() {
	dnsHostsMu.Lock()
	defer dnsHostsMu.Unlock()
	db := DB()
	var hypers []model.Hyper
	if err := db.Select("hostname, host_ip").Where("host_ip != ''").Find(&hypers).Error; err != nil {
		logger.Errorf("DNS: rebuild query failed: %v", err)
		return
	}

	hostsFile := dnsHostsFile()
	if err := os.MkdirAll(filepath.Dir(hostsFile), 0755); err != nil {
		logger.Errorf("DNS: mkdir failed: %v", err)
		return
	}

	var lines []string
	for _, h := range hypers {
		if h.Hostname != "" && h.HostIP != "" {
			lines = append(lines, h.HostIP+" "+h.Hostname)
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	tmpFile := filepath.Join(filepath.Dir(hostsFile), "."+filepath.Base(hostsFile)+".tmp")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		logger.Errorf("DNS: rebuild write failed: %v", err)
		return
	}
	if err := os.Rename(tmpFile, hostsFile); err != nil {
		logger.Errorf("DNS: rebuild rename failed: %v", err)
		return
	}
	logger.Infof("DNS: rebuilt %s with %d entries", hostsFile, len(lines))
}

package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ApplyDnsUpstream 将当前 DNS_UPSTREAM 设置写入 upstream.conf 并热重载 dnsmasq。
// 在 SyncSystemSettings 成功提交后异步调用，以及 clapi 启动时调用一次。
func ApplyDnsUpstream() {
	upstream := GetMirrorSetting("DNS_UPSTREAM")
	if upstream == "" {
		upstream = "8.8.8.8"
	}

	confDir := os.Getenv("DNS_CONF_DIR")
	if confDir == "" {
		confDir = "/opt/cloudland/dns/conf.d"
	}
	if err := os.MkdirAll(confDir, 0755); err != nil {
		logger.Errorf("DNS: mkdir %s failed: %v", confDir, err)
		return
	}
	confFile := filepath.Join(confDir, "upstream.conf")

	// 支持逗号分隔的多个上游地址
	var lines []string
	for _, addr := range strings.Split(upstream, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			lines = append(lines, fmt.Sprintf("server=%s", addr))
		}
	}
	if len(lines) == 0 {
		lines = []string{"server=8.8.8.8"}
	}
	content := strings.Join(lines, "\n") + "\n"

	// 原子写：先写临时文件再 rename，防止 dnsmasq SIGHUP 时读到截断文件
	tmpFile := confFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		logger.Errorf("DNS: failed to write %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, confFile); err != nil {
		logger.Errorf("DNS: rename %s -> %s failed: %v", tmpFile, confFile, err)
		return
	}

	logger.Infof("DNS: upstream set to %q", upstream)
	reloadDnsmasq()
}

// reloadDnsmasq 通过 Docker HTTP API 向 dnsmasq 容器发送 SIGHUP。
// clapi 容器挂载了 /var/run/docker.sock。
// 若 dnsmasq 未运行，curl 返回非零但被 || true 忽略。
func reloadDnsmasq() {
	cmd := `curl -sf --unix-socket /var/run/docker.sock ` +
		`-X POST 'http://localhost/containers/cloudland-dnsmasq/kill?signal=HUP' ` +
		`>/dev/null 2>&1`
	if err := exec.Command("sh", "-c", cmd).Run(); err != nil {
		logger.Infof("DNS: dnsmasq SIGHUP skipped (not running yet): %v", err)
	}
}

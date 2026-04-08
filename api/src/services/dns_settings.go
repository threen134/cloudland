package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	tmpFile := filepath.Join(filepath.Dir(confFile), "."+filepath.Base(confFile)+".tmp")
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

// dockerClient 复用连接池，避免每次调用重建 Transport。
var dockerClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		},
	},
}

// reloadDnsmasq 通过 Docker HTTP API 向 dnsmasq 容器发送 SIGHUP。
// clapi 容器挂载了 /var/run/docker.sock，使用 Go 原生 HTTP over Unix socket，
// 不依赖外部命令。若 dnsmasq 未运行，API 返回 404/500 视为正常，仅记录 Info。
func reloadDnsmasq() {
	resp, err := dockerClient.Post(
		"http://localhost/containers/cloudland-dnsmasq/kill?signal=HUP",
		"", nil,
	)
	if err != nil {
		logger.Infof("DNS: dnsmasq SIGHUP skipped (not running yet): %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		logger.Infof("DNS: dnsmasq SIGHUP returned status %d", resp.StatusCode)
	}
}

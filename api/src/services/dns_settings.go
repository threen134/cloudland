package services

import (
	"context"
	"encoding/json"
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
// dnsmasq 以 --servers-file 加载 upstream.conf，SIGHUP 时会重读（conf-dir/conf-file 里的 server= 不会重读）。
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

// reloadDnsmasq 通过 Docker exec API 在 dnsmasq 容器内执行 kill -HUP 1（容器内 PID 1 即 dnsmasq）。
// 不用 Docker 的 kill API：即使只发 SIGHUP，它也会把容器标记为手动停止（HasBeenManuallyStopped），
// restart: unless-stopped 的 dnsmasq 在 Docker/宿主机重启后就不会被拉起。
// clapi 容器挂载了 /var/run/docker.sock；dnsmasq 未运行时 Docker 返回 404/409，仅记录 Info。
func reloadDnsmasq() {
	resp, err := dockerClient.Post(
		"http://localhost/containers/cloudland-dnsmasq/exec",
		"application/json", strings.NewReader(`{"Cmd":["kill","-HUP","1"]}`),
	)
	if err != nil {
		logger.Infof("DNS: dnsmasq reload skipped (docker API unavailable): %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		logger.Infof("DNS: dnsmasq reload skipped, create exec returned status %d", resp.StatusCode)
		return
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID == "" {
		logger.Infof("DNS: dnsmasq reload skipped, unexpected exec response: %v", err)
		return
	}
	startResp, err := dockerClient.Post(
		"http://localhost/exec/"+created.ID+"/start",
		"application/json", strings.NewReader(`{"Detach":true}`),
	)
	if err != nil {
		logger.Infof("DNS: dnsmasq reload failed to start exec: %v", err)
		return
	}
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		logger.Infof("DNS: dnsmasq reload start exec returned status %d", startResp.StatusCode)
	}
}

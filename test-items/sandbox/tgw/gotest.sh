#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
command -v gcc >/dev/null || apt-get install -y -qq gcc libc6-dev >/dev/null 2>&1
if [ ! -x /usr/local/go/bin/go ]; then
    ver=$(curl -s 'https://go.dev/VERSION?m=text' | head -1)
    case "$ver" in go1.2[6-9]*|go1.3*) ;; *) ver=go1.26.0 ;; esac
    curl -sSL -o /tmp/go.tgz https://go.dev/dl/$ver.linux-amd64.tar.gz
    rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:$PATH
go version
R="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
rm -rf /root/cl && mkdir -p /root/cl
cp -r "$R/cpgateway" /root/cl/
cp -r "$R/api" /root/cl/
find /root/cl -name '*.go' -exec sed -i 's/\r$//' {} +
cd /root/cl/cpgateway && CGO_ENABLED=1 go test ./... -count=1 2>&1 | grep -E '^(ok|FAIL|---|panic)' | head -30
cd /root/cl/api && CGO_ENABLED=1 go test ./src/dbs/ ./src/utils/tracing/ -count=1 2>&1 | grep -E '^(ok|FAIL|---|panic)' | head

#!/bin/bash
# 基于 deploy/docker/.run.sh 配置的监控信息提取

PUBLIC_IP="165.192.110.235"
ADMIN_USER="admin@local.com"
ADMIN_PASS="AgFFTFV8AzK4FG0"

echo "==== 1. 登录并获取 Token ===="
TOKEN=$(curl -sk https://${PUBLIC_IP}/api/v1/auth/token \
  -X POST \
  -H "Content-Type: application/json" \
  -d "{\"username\": \"$ADMIN_USER\", \"password\": \"$ADMIN_PASS\"}" \
  | jq -r '.access_token')

if [ "$TOKEN" == "null" ] || [ -z "$TOKEN" ]; then
    echo "登录失败，无法连接到 $PUBLIC_IP"
    exit 1
fi
echo "获取 Token 成功。"

echo "==== 2. 获取实例 (VM) 列表 ===="
INSTANCES=$(curl -sk https://${PUBLIC_IP}/api/v1/instances -H "Authorization: Bearer $TOKEN")

# 检查是否有实例
INSTANCE_COUNT=$(echo "$INSTANCES" | jq '.instances | length')
if [ "$INSTANCE_COUNT" -eq 0 ] || [ "$INSTANCE_COUNT" == "null" ]; then
    echo "当前没有正在运行的实例。"
    exit 0
fi

VM_UUID=$(echo "$INSTANCES" | jq -r '.instances[0].uuid')
VM_IFACE=$(echo "$INSTANCES" | jq -r '.instances[0].interfaces[0].device_name // "eth0"')

echo "正在拉取实例 [$VM_UUID] (网卡: $VM_IFACE) 的最近 10 分钟监控数据..."

NOW=$(date +%s)
START=$((NOW - 600))

payload_basic="{\"start\": \"$START\", \"end\": \"$NOW\", \"step\": \"60s\", \"id\": [\"$VM_UUID\"]}"

echo "---- CPU History ----"
curl -sk "https://${PUBLIC_IP}/api/v1/metrics/instances/cpu/his_data" \
  -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$payload_basic" | jq .

echo "---- Memory History ----"
curl -sk "https://${PUBLIC_IP}/api/v1/metrics/instances/memory/his_data" \
  -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$payload_basic" | jq .

# 系统配置说明

## cland 配置 (`/etc/cloudland/cland.conf`)

控制面服务的核心配置文件。

| 配置项 | 示例 | 描述 |
|--------|------|------|
| db_host | `127.0.0.1` | 数据库主机地址 |
| db_port | `5432` | 数据库端口 |
| db_user | `postgres` | 数据库账户 |
| log_level | `debug` | 日志级别 (debug/info/warn/error) |

## rpcworker 配置 (`/etc/cloudland/rpcworker.conf`)

负责 API 与 控制面 之间的 RPC 交互。

```ini
[global]
listen = :50080
auth_token = YOUR_SECRET_AUTH_TOKEN
```

## 网络配置 (OVS)

桥接网络接口定义。

```bash
# 修改 /etc/network/interfaces
auto br0
iface br0 inet dhcp
    ovs_type OVSBridge
    ovs_ports eth0
```

# 快速部署手册

## 最小化单机部署

在支持 KVM 虚拟化的单机环境下，您可以通过以下步骤快速启动一个 CloudLand 全家桶系统。

### 1. 基础环境检查

```bash
# 检查是否支持 KVM
grep -E 'vmx|svm' /proc/cpuinfo
# 安装 OVS 依赖
sudo apt update && sudo apt install openvswitch-switch -y
```

### 2. 下载并初始化数据库

CloudLand 依赖 PostgreSQL。

```bash
docker run --name cloudland-db -e POSTGRES_PASSWORD=cloudland -p 5432:5432 -d postgres:14
```

### 3. 配置与运行

1. 下载最新的发行版二进制包。
2. 编辑 `cland.conf` 指定数据库连接。
3. 依次启动系统核心组件：
   - `cland` (控制面)
   - `sci` (消息总线)
   - `cloudlet` (计算代理)
   - `api-server` (REST 接口)

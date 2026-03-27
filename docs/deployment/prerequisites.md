# 部署前置要求

## 硬件配置 (建议)

针对控制面节点 (Control Plane) 和计算节点 (Hypervisor)。

| 组件类型 | CPU | 内存 | 存储 |
|---------|-----|------|------|
| 控制节点 | 4 核 | 8 GB | 100 GB NVMe |
| 计算节点 | 8 核+ | 16 GB+ | 500 GB+ SSD |

## 操作系统

目前 CloudLand 主要在如下发行版经过充分测试：
- **Ubuntu 22.04 LTS** (强烈推荐)
- **CentOS Stream 9**
- **Debian 12**

## 核心依赖组件

- **libvirt**: 8.0+
- **Open vSwitch**: 2.17+
- **QEMU/KVM**: 6.1+
- **PostgreSQL**: 14+
- **Redis**: 6.2+

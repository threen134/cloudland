# 虚拟机实例指南

## 概述

CloudLand 的虚拟机实例支持弹性创建、生命周期管理以及实时监控。

### 核心功能

- **一键部署**：从公共或私有镜像快速启动实例。
- **自定义规格**：灵活配置 CPU、内存与系统磁盘。
- **弹性扩展**：支持在线迁移与规格变更（Flavor Resize）。
- **生命周期控制**：开机、关机、重启、挂起、快照。

## 常用操作

### 创建实例

创建实例时，您可以选择：
1. **可用区覆盖** (Availability Zone)
2. **实例模板** (Flavor)
3. **主网络接口** (Primary Network)

```bash
# API 示例预览
POST /api/v1/instances {
  "name": "web-server",
  "flavor_id": "c1-m2-d40",
  "image_id": "ubuntu-22.04",
  "network_id": "vpc-001-net"
}
```

## 实例监控

集成了 Grafana 仪表盘，可以实时查看 CPU 使用率、网络 IO 以及磁盘读写状态。

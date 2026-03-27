# 负载均衡 (LB)

## 功能简介

CloudLand 提供四层 (TCP/UDP) 负载均衡能力，基于 LVS/HAProxy 技术实现高并发分发。

### 关键组件

- **监听器 (Listener)**：定义监听端口与协议。
- **后端资源池 (Pool)**：包含一组待分发的虚拟机实例。
- **健康检查 (Monitor)**：自动剔除异常节点。

## 转发算法

目前支持如下调度策略：
- **轮询 (Round Robin)**
- **最少连接 (Least Connections)**
- **源 IP 散列 (Source IP Hash)**

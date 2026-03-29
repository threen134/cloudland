# 系统架构 - 计算节点生命周期

## 节点生命周期管理 (Lifecycle)

CloudLand 对 Hypervisor 的管理采用全生命周期监控机制。

### 典型状态流

```mermaid
stateDiagram-v2
    direction LR

    [*] --> disabled: 手动注册
    disabled --> active: 管理员启用
    active --> draining: 开启维护模式
    draining --> decommissioned: 资源驱逐完成
    active --> error: 心跳探测超时
    error --> active: 链路恢复正常
    decommissioned --> [*]: 释放资源

    state active {
        style active fill:rgba(14, 165, 233, 0.2),stroke:#0ea5e9
    }
    state error {
        style error fill:rgba(244, 63, 94, 0.1),stroke:#f43f5e
    }
```

## 关键流程：节点注册

1. **探测并自注册**：`cloudlet` 启动后自动上报宿主机 CPU/内存/磁盘性能。
2. **连接鉴权**：`sci` 处理安全握手，确立加密通信通道。
3. **分配网络**：根据集群 OVS 规划分配基础网络资源。

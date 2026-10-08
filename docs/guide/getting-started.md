# 快速上手

## 环境准备

从源码构建需要：

| 依赖 | 版本要求 |
|------|---------|
| Go | ≥ 1.25 |
| Node.js | ≥ 20 |
| PostgreSQL | ≥ 14 |

计算节点上另需 libvirt ≥ 8.0、QEMU/KVM、`socat`；这些由部署脚本自动安装，见 [添加计算节点](../deployment/04-compute-node.md)。

## 快速构建

### 1. 克隆仓库

```bash
git clone https://github.com/threen134/cloudland.git
cd cloudland
```

### 2. 构建后端

```bash
# 区域服务：clapi、cland、cloudlet、告警规则管理
cd api && make setup && make

# 中央网关
cd cpgateway && make build
```

### 3. 构建前端

```bash
cd web
npm install
npm run build
```

## 部署

实际部署不需要手工构建：一条命令即可拉起全量单节点环境，详见 [部署文档](../deployment/index.md)。

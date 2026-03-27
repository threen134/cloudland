# 快速开始

## 环境要求

| 依赖 | 版本要求 |
|------|---------|
| Go | ≥ 1.21 |
| Node.js | ≥ 18 |
| PostgreSQL | ≥ 14 |
| GCC / G++ | ≥ 11 |
| libvirt | ≥ 8.0 |
| Open vSwitch | ≥ 2.17 |

## 快速构建

### 1. 克隆仓库

```bash
git clone https://github.com/maplerime/cloudland.git
cd cloudland
```

### 2. 构建 API 服务

```bash
cd api
make build
```

### 3. 构建前端

```bash
cd web
npm install
npm run build
```

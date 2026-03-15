# Cloudland Control Plane Gateway (中间件平台)

本项目是一个基于 Python FastAPI 的中间件服务，作为前端与各区域云平台后端的桥梁。其核心功能是实现跨区域的请求代理、统一身份验证以及按需开户。

---

## 快速开始 (使用 Docker)

为了简化环境配置，推荐使用 Docker 运行完整开发环境。

### 1. 准备环境文件
复制 `.env.example` 并生成 `.env` 文件：
```bash
cp .env.example .env
```
*注：本项目无默认配置，必须确保 `.env` 中的 `SECRET_KEY` 等关键项已设置。*

### 2. 启动容器
在项目根目录下，使用以下命令启动服务：
```bash
cd docker
docker-compose up -d
```
该命令会同时启动：
- **cloudland-cpgateway**: 核心 API 服务 [http://localhost:8000](http://localhost:8000)
- **cloudland-mailhog**: 测试用邮件捕获工具 [http://localhost:8025](http://localhost:8025)

### 3. 查看运行日志
```bash
docker logs -f cloudland-cpgateway
```

---

## 本地开发运行

如果你希望在本地直接运行：

### 1. 环境准备
```bash
# 创建虚拟环境
python3 -m venv venv
# 激活环境
source venv/bin/activate  # macOS/Linux
# 安装依赖
pip install -r requirements.txt
```

### 2. 启动服务
```bash
python -m app.main
```

---

## API 文档与入口

- **交互式文档 (Swagger)**: [http://localhost:8000/docs](http://localhost:8000/docs)
- **核心流程说明**:
    - **注册**: `POST /auth/register` - 创建本地账号并发送激活邮件。
    - **激活**: 通过邮件链接或调用接口激活，激活后会自动同步到所有预设区域。
    - **透明代理**: 携带 JWT 并在 URL 中提供 `region` 参数，中间件会自动路由至对应后端。

## 调试说明

- **日志等级**: 默认支持 `DEBUG` 和 `INFO` 模式。业务流水日志已通过 `INFO` 级标记。
- **邮件查看**: 所有的激活邮件均会发送到本地的 **MailHog**，请访问 `http://localhost:8025` 查看。
- **数据库**: 开发环境默认使用 SQLite (`cloudland.db`)。

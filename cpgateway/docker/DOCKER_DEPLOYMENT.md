# Docker 部署指南

本文档说明如何使用 Docker 部署 Cloudland 中间平台服务。

## 前置要求

- Docker 20.10+
- Docker Compose 2.0+

## 快速开始(开发环境)

### 1. 使用 Docker Compose 启动服务

```bash
# 构建并启动服务
docker-compose up -d

# 查看日志
docker-compose logs -f cpgateway

# 停止服务
docker-compose down
```

服务将在 `http://localhost:8000` 启动。

### 2. 使用 Dockerfile 单独构建

```bash
# 构建镜像
docker build -t cloudland-cpgateway:latest .

# 运行容器
docker run -d \
  --name cloudland-cpgateway \
  -p 8000:8000 \
  -v $(pwd)/data:/app/data \
  cloudland-cpgateway:latest

# 查看日志
docker logs -f cloudland-cpgateway

# 停止容器
docker stop cloudland-cpgateway
docker rm cloudland-cpgateway
```

## 生产环境部署

### 使用 PostgreSQL 数据库

```bash
# 使用生产环境配置
docker-compose -f docker-compose.prod.yml up -d

# 查看服务状态
docker-compose -f docker-compose.prod.yml ps

# 查看日志
docker-compose -f docker-compose.prod.yml logs -f

# 停止服务
docker-compose -f docker-compose.prod.yml down
```

### 环境变量配置

创建 `.env` 文件:

```bash
# 云平台 API 配置
CLOUD_PLATFORM_API_KEY=your_production_api_key_here

# 数据库配置(如果需要自定义)
POSTGRES_USER=postgres
POSTGRES_PASSWORD=your_secure_password
POSTGRES_DB=cloudland
```

## 数据持久化

### SQLite (开发环境)

数据库文件存储在 `./data/cloudland.db`,通过 volume 挂载持久化。

### PostgreSQL (生产环境)

数据存储在 Docker volume `postgres-data` 中,自动持久化。

## 常用命令

### 查看运行中的容器

```bash
docker ps
```

### 进入容器 Shell

```bash
docker exec -it cloudland-cpgateway bash
```

### 查看容器日志

```bash
# 实时查看日志
docker logs -f cloudland-cpgateway

# 查看最近 100 行日志
docker logs --tail 100 cloudland-cpgateway
```

### 重启服务

```bash
docker-compose restart cpgateway
```

### 重新构建镜像

```bash
# 重新构建并启动
docker-compose up -d --build

# 或者
docker-compose build --no-cache
docker-compose up -d
```

## 测试 API

### 注册用户

```bash
curl -X POST "http://localhost:8000/api/v1/auth/register" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "username": "testuser",
    "password": "testpass123"
  }'
```

### 用户登录

```bash
curl -X POST "http://localhost:8000/api/v1/auth/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=testuser&password=testpass123"
```

### 访问 API 文档

打开浏览器访问: http://localhost:8000/docs

## 故障排查

### 容器无法启动

```bash
# 查看详细日志
docker-compose logs cpgateway

# 检查容器状态
docker-compose ps
```

### 端口冲突

如果 8000 端口被占用,修改 `docker-compose.yml` 中的端口映射:

```yaml
ports:
  - "8001:8000"  # 将主机端口改为 8001
```

### 数据库连接失败

检查环境变量配置和数据库服务状态:

```bash
# 检查 PostgreSQL 是否运行
docker-compose ps postgres

# 查看 PostgreSQL 日志
docker-compose logs postgres
```

## 清理资源

### 停止并删除所有容器

```bash
docker-compose down
```

### 删除数据卷(谨慎操作!)

```bash
# 这将删除所有数据!
docker-compose down -v
```

### 删除镜像

```bash
docker rmi cloudland-cpgateway:latest
```

## 性能优化

### 多阶段构建(可选)

如需优化镜像大小,可以使用多阶段构建。修改 `Dockerfile`:

```dockerfile
# 构建阶段
FROM python:3.12-slim as builder
WORKDIR /app
COPY requirements.txt .
RUN pip install --user --no-cache-dir -r requirements.txt

# 运行阶段
FROM python:3.12-slim
WORKDIR /app
COPY --from=builder /root/.local /root/.local
COPY ./app ./app
ENV PATH=/root/.local/bin:$PATH
EXPOSE 8000
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
```

## 安全建议

1. **不要在生产环境中使用默认密码**
2. **使用环境变量管理敏感信息**
3. **定期更新基础镜像**
4. **限制容器资源使用**
5. **使用非 root 用户运行容器**

## 监控和日志

### 查看资源使用情况

```bash
docker stats cloudland-cpgateway
```

### 导出日志

```bash
docker logs cloudland-cpgateway > cpgateway.log 2>&1
```

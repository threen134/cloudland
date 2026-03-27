# Docker 容器化部署

## 快速指南

通过 Docker Compose 快速运行所有微服务组件。

### Docker Compose 配置

```yaml
version: '3.8'
services:
  db:
    image: postgres:14
    environment:
      POSTGRES_DB: cloudland
      POSTGRES_PASSWORD: password

  cland:
    image: cloudland/cland:latest
    depends_on:
      - db
    volumes:
      - ./config:/etc/cloudland

  api:
    image: cloudland/api:latest
    ports:
      - "8255:8255"
```

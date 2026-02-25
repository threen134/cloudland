# =============================================================================
# SCI 库编译基础镜像
# 用途: 为 cloudland 主控提供 SCI 通信库
# 构建: docker build -f dockerfiles/Dockerfile.sci -t cloudland-sci ../../
# =============================================================================

FROM ubuntu:22.04 AS sci-builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    autoconf \
    automake \
    libtool \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 复制 SCI 源码
COPY sci/ /opt/cloudland/sci/

WORKDIR /opt/cloudland/sci

RUN ./configure && make && make install

# 最终产物在 /opt/sci/ (lib64/, include/, bin/, sbin/)

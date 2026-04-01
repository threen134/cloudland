#!/bin/bash
# =============================================================================
# CloudLand Docker 部署 - 证书初始化脚本
# 合并了 web/gencert.sh + nginx/gencert.sh + console_cert.sh
# 用法: ./init-certs.sh [CERT_DIR]
# =============================================================================

set -e

CERT_DIR="${1:-./volumes/certs}"
mkdir -p "$CERT_DIR"/{cland,nginx,console,region-gw}

echo "==> 检查证书工具..."
if ! command -v certtool &>/dev/null; then
    echo "未检测到 certtool，尝试自动安装 (gnutls-bin)..."
    if command -v apt-get &>/dev/null; then
        sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y gnutls-bin
    elif command -v yum &>/dev/null; then
        sudo yum install -y gnutls-utils
    else
        echo "错误: 无法确定包管理器。请手动安装 gnutls-bin 或 gnutls-utils"
        exit 1
    fi
fi

# ----- 1. CloudLand Web 证书 (clbase / clapi 使用) -----
if [ -e "$CERT_DIR/cland/selfsigned.key" ] && [ -e "$CERT_DIR/cland/selfsigned.crt" ]; then
    echo "==> CloudLand 证书已存在，跳过"
else
    echo "==> 生成 CloudLand 自签名证书..."
    cat > /tmp/cland-cert.info <<EOF
organization = cloudland
ip_address = 127.0.0.1
dns_name = alarm-rules-mgr
dns_name = clapi
dns_name = cloudland-clapi
dns_name = cloudland-alarm-rules-mgr
tls_www_server
ca
encryption_key
signing_key
EOF
    certtool --generate-privkey --outfile "$CERT_DIR/cland/selfsigned.key" > /dev/null 2>&1
    certtool --generate-self-signed \
        --load-privkey "$CERT_DIR/cland/selfsigned.key" \
        --template /tmp/cland-cert.info \
        --outfile "$CERT_DIR/cland/selfsigned.crt" > /dev/null 2>&1
    rm -f /tmp/cland-cert.info
    echo "    完成: $CERT_DIR/cland/"
fi

# ----- 1.5 CloudLand JWT 密钥 (clbase / clapi 使用) -----
if [ -e "$CERT_DIR/cland/jwt_private.pem" ] && [ -e "$CERT_DIR/cland/jwt_public.pem" ]; then
    echo "==> CloudLand JWT 密钥已存在，跳过"
else
    echo "==> 生成 CloudLand JWT RSA256 密钥对..."
    openssl genpkey -algorithm RSA -out "$CERT_DIR/cland/jwt_private.pem" -pkeyopt rsa_keygen_bits:2048 > /dev/null 2>&1
    openssl rsa -pubout -in "$CERT_DIR/cland/jwt_private.pem" -out "$CERT_DIR/cland/jwt_public.pem" > /dev/null 2>&1
    echo "    完成: $CERT_DIR/cland/jwt_*.pem"
fi

# ----- 2. Nginx 证书 -----
if [ -e "$CERT_DIR/nginx/selfsigned.key" ] && [ -e "$CERT_DIR/nginx/selfsigned.crt" ] && [ -e "$CERT_DIR/nginx/dhparam.pem" ]; then
    echo "==> Nginx 证书已存在，跳过"
else
    echo "==> 生成 Nginx 自签名证书..."
    cat > /tmp/nginx-cert.info <<EOF
organization = cloudland
tls_www_server
encryption_key
signing_key
EOF
    certtool --generate-privkey --outfile "$CERT_DIR/nginx/selfsigned.key" > /dev/null 2>&1
    certtool --generate-self-signed \
        --load-privkey "$CERT_DIR/nginx/selfsigned.key" \
        --template /tmp/nginx-cert.info \
        --outfile "$CERT_DIR/nginx/selfsigned.crt" > /dev/null 2>&1
    rm -f /tmp/nginx-cert.info
    echo "    生成 DH 参数（可能需要几分钟）..."
    openssl dhparam -out "$CERT_DIR/nginx/dhparam.pem" 2048 > /dev/null 2>&1
    echo "    完成: $CERT_DIR/nginx/"
fi

# ----- 3. Console Proxy 证书 -----
if [ -e "$CERT_DIR/console/cacert.pem" ] && [ -e "$CERT_DIR/console/servercert.pem" ]; then
    echo "==> Console Proxy 证书已存在，跳过"
else
    echo "==> 生成 Console Proxy 证书..."
    cat > /tmp/console-ca.info <<EOF
cn = console-proxy
ca
cert_signing_key
EOF
    certtool --generate-privkey --outfile "$CERT_DIR/console/cakey.pem" > /dev/null 2>&1
    certtool --generate-self-signed \
        --load-privkey "$CERT_DIR/console/cakey.pem" \
        --template /tmp/console-ca.info \
        --outfile "$CERT_DIR/console/cacert.pem" > /dev/null 2>&1

    certtool --generate-privkey --outfile "$CERT_DIR/console/serverkey.pem" > /dev/null 2>&1
    cat > /tmp/console-server.info <<EOF
organization = console-proxy
cn = cloudland
tls_www_server
encryption_key
signing_key
EOF
    certtool --generate-certificate \
        --load-privkey "$CERT_DIR/console/serverkey.pem" \
        --load-ca-certificate "$CERT_DIR/console/cacert.pem" \
        --load-ca-privkey "$CERT_DIR/console/cakey.pem" \
        --template /tmp/console-server.info \
        --outfile "$CERT_DIR/console/servercert.pem" > /dev/null 2>&1
    rm -f /tmp/console-ca.info /tmp/console-server.info
    echo "    完成: $CERT_DIR/console/"
fi

# ----- 4. Region Gateway 证书 (多 Region 部署的 VNC WebSocket 边缘入口) -----
if [ -e "$CERT_DIR/region-gw/selfsigned.key" ] && [ -e "$CERT_DIR/region-gw/selfsigned.crt" ]; then
    echo "==> Region Gateway 证书已存在，跳过"
else
    echo "==> 生成 Region Gateway 自签名证书..."
    cat > /tmp/region-gw-cert.info <<EOF
organization = cloudland
cn = cloudland-region-gateway
tls_www_server
encryption_key
signing_key
EOF
    certtool --generate-privkey --outfile "$CERT_DIR/region-gw/selfsigned.key" > /dev/null 2>&1
    certtool --generate-self-signed \
        --load-privkey "$CERT_DIR/region-gw/selfsigned.key" \
        --template /tmp/region-gw-cert.info \
        --outfile "$CERT_DIR/region-gw/selfsigned.crt" > /dev/null 2>&1
    rm -f /tmp/region-gw-cert.info
    echo "    完成: $CERT_DIR/region-gw/"
fi

echo ""
echo "=== 所有证书已就绪 ==="
echo "  CloudLand:       $CERT_DIR/cland/"
echo "  Nginx:           $CERT_DIR/nginx/"
echo "  Console Proxy:   $CERT_DIR/console/"
echo "  Region Gateway:  $CERT_DIR/region-gw/"

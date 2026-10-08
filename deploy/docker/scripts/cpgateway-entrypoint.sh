#!/bin/sh
set -e

echo "=== CloudLand Control Plane Gateway (Go) Entrypoint ==="

# 检查弱密钥警告
if [ -z "${AUTH_SECRET_KEY:-}" ] || [ "${AUTH_SECRET_KEY}" = "change-me-in-production" ]; then
  echo "⚠️  WARNING: Using default AUTH_SECRET_KEY is INSECURE for production!"
  echo "⚠️  Please set CPGATEWAY_SECRET_KEY in your .env file"
fi

# 等待数据库就绪（表结构由程序启动时 GORM AutoMigrate 自动创建）
if [ -n "${DB_URI:-}" ]; then
  echo "Waiting for database to be ready..."
  MAX_RETRIES=30
  RETRY_COUNT=0
  until pg_isready -d "$DB_URI" -t 5 > /dev/null 2>&1; do
    RETRY_COUNT=$((RETRY_COUNT + 1))
    if [ $RETRY_COUNT -ge $MAX_RETRIES ]; then
      echo "❌ ERROR: Database connection failed after $MAX_RETRIES attempts"
      exit 1
    fi
    echo "Database is unavailable - sleeping (attempt $RETRY_COUNT/$MAX_RETRIES)"
    sleep 2
  done
  echo "✓ Database is ready!"
fi

# 生成 JWT RS256 密钥对（如果不存在）
RSA_PRIVATE_KEY="${AUTH_RSA_PRIVATE_KEY_PATH:-/app/keys/private.pem}"
RSA_PUBLIC_KEY="${AUTH_RSA_PUBLIC_KEY_PATH:-/app/keys/public.pem}"

if [ ! -s "$RSA_PRIVATE_KEY" ] || [ ! -s "$RSA_PUBLIC_KEY" ]; then
  echo "Generating RSA key pair for JWT signing..."
  mkdir -p "$(dirname "$RSA_PRIVATE_KEY")" "$(dirname "$RSA_PUBLIC_KEY")"
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$RSA_PRIVATE_KEY" > /dev/null 2>&1
  openssl pkey -in "$RSA_PRIVATE_KEY" -pubout -out "$RSA_PUBLIC_KEY"
  chmod 600 "$RSA_PRIVATE_KEY"
  echo "✓ RSA key pair generated successfully"
else
  echo "✓ RSA key pair already exists"
fi

echo "Starting Control Plane Gateway application..."
exec "$@"

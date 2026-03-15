#!/bin/sh
set -e

echo "=== CloudLand Control Plane Gateway Entrypoint ==="

# 检查弱密钥警告
if [ "${SECRET_KEY:-}" = "change-me-in-production" ]; then
  echo "⚠️  WARNING: Using default SECRET_KEY is INSECURE for production!"
  echo "⚠️  Please set CPGATEWAY_SECRET_KEY in your .env file"
fi

# 等待数据库就绪
echo "Waiting for database to be ready..."
MAX_RETRIES=30
RETRY_COUNT=0

until python -c "import asyncpg, asyncio, os; dsn=os.environ['DATABASE_URL'].replace('postgresql+asyncpg://','postgresql://'); asyncio.run(asyncpg.connect(dsn, timeout=5))" 2>&1; do
  RETRY_COUNT=$((RETRY_COUNT + 1))
  if [ $RETRY_COUNT -ge $MAX_RETRIES ]; then
    echo "❌ ERROR: Database connection failed after $MAX_RETRIES attempts"
    echo "DATABASE_URL: ${DATABASE_URL}"
    exit 1
  fi
  echo "Database is unavailable - sleeping (attempt $RETRY_COUNT/$MAX_RETRIES)"
  sleep 2
done

echo "✓ Database is ready!"
echo "Note: Database tables will be created automatically on application startup"

# 生成 RSA 密钥对（如果不存在）
RSA_PRIVATE_KEY="/app/keys/private.pem"
RSA_PUBLIC_KEY="/app/keys/public.pem"

if [ ! -f "$RSA_PRIVATE_KEY" ] || [ ! -f "$RSA_PUBLIC_KEY" ]; then
  echo "Generating RSA key pair for JWT signing..."
  python3 << 'PYTHON_SCRIPT'
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.backends import default_backend

# Generate RSA key pair
private_key = rsa.generate_private_key(
    public_exponent=65537,
    key_size=2048,
    backend=default_backend()
)

# Save private key
with open('/app/keys/private.pem', 'wb') as f:
    f.write(private_key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption()
    ))

# Save public key
public_key = private_key.public_key()
with open('/app/keys/public.pem', 'wb') as f:
    f.write(public_key.public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo
    ))

print("✓ RSA key pair generated successfully")
PYTHON_SCRIPT
else
  echo "✓ RSA key pair already exists"
fi

# 启动应用（应用会在 startup 事件中自动创建表）
echo "Starting Control Plane Gateway application..."
exec "$@"

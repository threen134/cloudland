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

# 运行数据库迁移
echo "Running database migrations..."
cd /app
if [ -f alembic.ini ]; then
  # Check if alembic_version table exists (i.e., migrations have been initialized)
  HAS_VERSION=$(python -c "
import asyncio, asyncpg, os
async def check():
    dsn = os.environ['DATABASE_URL'].replace('postgresql+asyncpg://','postgresql://')
    conn = await asyncpg.connect(dsn, timeout=5)
    try:
        await conn.fetchval('SELECT 1 FROM alembic_version LIMIT 1')
        return True
    except asyncpg.exceptions.UndefinedTableError:
        return False
    finally:
        await conn.close()
print(asyncio.run(check()))
" 2>/dev/null || echo "False")

  if [ "$HAS_VERSION" = "False" ]; then
    # No alembic_version table — check if this is a fresh DB or an existing one
    HAS_TABLES=$(python -c "
import asyncio, asyncpg, os
async def check():
    dsn = os.environ['DATABASE_URL'].replace('postgresql+asyncpg://','postgresql://')
    conn = await asyncpg.connect(dsn, timeout=5)
    try:
        count = await conn.fetchval(\"SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'\")
        return count > 0
    finally:
        await conn.close()
print(asyncio.run(check()))
" 2>/dev/null || echo "False")

    if [ "$HAS_TABLES" = "False" ]; then
      # Fresh database: create all tables from models, stamp at head
      echo "Fresh database: creating all tables..."
      python -c "
import asyncio
from app.core.database import engine, Base
from app.models import *  # noqa: F401,F403
async def init():
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
asyncio.run(init())
"
      python -m alembic stamp head
      echo "✓ Database initialized with all tables and stamped at head"
    else
      # Existing database without alembic: stamp baseline, then upgrade
      echo "Existing database detected, initializing alembic from baseline..."
      python -m alembic stamp 001
      if ! python -m alembic upgrade head; then
        echo "⚠️  WARNING: Migration failed, application will start but database may be out of sync"
        echo "⚠️  Please run migrations manually: docker exec <container> python -m alembic upgrade head"
      else
        echo "✓ Database migrated from baseline to head"
      fi
    fi
  else
    # alembic_version exists: run pending migrations
    if ! python -m alembic upgrade head; then
      echo "⚠️  WARNING: Migration failed, application will start but database may be out of sync"
      echo "⚠️  Please run migrations manually: docker exec <container> python -m alembic upgrade head"
    else
      echo "✓ Database migrations complete"
    fi
  fi
else
  echo "Note: No alembic.ini found, tables will be created on application startup"
fi

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

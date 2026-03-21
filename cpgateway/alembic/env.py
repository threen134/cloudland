"""
Alembic migration environment for CloudLand CPGateway.

Supports both online (async PostgreSQL) and offline migration modes.
Database URL is loaded from app config, no need to hardcode in alembic.ini.
"""

import asyncio
from logging.config import fileConfig

from sqlalchemy import pool
from sqlalchemy.ext.asyncio import create_async_engine

from alembic import context

# Load alembic.ini logging config
config = context.config
if config.config_file_name is not None:
    fileConfig(config.config_file_name)

# Import all models so metadata includes all tables
from app.models import *  # noqa: F401,F403
from app.core.database import Base

target_metadata = Base.metadata

# Get database URL from app config
from app.core.config import settings
db_url = settings.async_database_url


def run_migrations_offline():
    """Run migrations in 'offline' mode (generate SQL without DB connection)."""
    # Convert async URL to sync for offline mode
    sync_url = db_url.replace("postgresql+asyncpg://", "postgresql://")
    context.configure(
        url=sync_url,
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
    )
    with context.begin_transaction():
        context.run_migrations()


def do_run_migrations(connection):
    context.configure(
        connection=connection,
        target_metadata=target_metadata,
        compare_type=True,
    )
    with context.begin_transaction():
        context.run_migrations()


async def run_async_migrations():
    """Run migrations in 'online' mode with async engine."""
    connectable = create_async_engine(db_url, poolclass=pool.NullPool)
    async with connectable.connect() as connection:
        await connection.run_sync(do_run_migrations)
    await connectable.dispose()


def run_migrations_online():
    """Run migrations in 'online' mode."""
    asyncio.run(run_async_migrations())


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()

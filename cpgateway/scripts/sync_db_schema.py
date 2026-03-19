import asyncio
from sqlalchemy import text
from app.core.database import engine

async def sync_db():
    """
    仅适用于 SQLite 开发环境（使用 PRAGMA / ALTER TABLE ADD COLUMN）。
    生产环境（PostgreSQL）请使用 Alembic 迁移：
        alembic revision --autogenerate -m "add maintenance_mode"
        alembic upgrade head
    """
    print("Syncing database schema...")
    async with engine.begin() as conn:
        # Check current columns in regions
        result = await conn.execute(text("PRAGMA table_info(regions)"))
        columns = [row[1] for row in result.fetchall()]
        print(f"Current columns: {columns}")

        # Add missing columns if they don't exist
        needed_columns = {
            "display_name": "VARCHAR(128)",
            "internal_endpoint": "VARCHAR(512)",
            "internal_secret": "VARCHAR(256)",
            "is_available": "BOOLEAN DEFAULT 1",
            "last_check_at": "DATETIME",
            "status_message": "VARCHAR(255)",
            "fail_count": "INTEGER DEFAULT 0",
            "maintenance_mode": "BOOLEAN DEFAULT 0"
        }

        for col, col_type in needed_columns.items():
            if col not in columns:
                print(f"Adding column {col}...")
                try:
                    await conn.execute(text(f"ALTER TABLE regions ADD COLUMN {col} {col_type}"))
                except Exception as e:
                    print(f"Failed to add {col}: {e}")

        # If endpoint_url exists but internal_endpoint was just added, 
        # try to migrate data if needed, but here we just ensure the schema is correct.
        if "endpoint_url" in columns and "internal_endpoint" in needed_columns:
            print("Migrating endpoint_url to internal_endpoint...")
            await conn.execute(text("UPDATE regions SET internal_endpoint = endpoint_url WHERE internal_endpoint IS NULL"))

    print("Database sync complete.")

if __name__ == "__main__":
    asyncio.run(sync_db())

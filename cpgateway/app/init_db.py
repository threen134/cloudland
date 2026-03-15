
import asyncio
from app.core.database import engine, Base
from app.models.user import User  # Ensure models are imported

async def init_db():
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.drop_all)
        await conn.run_sync(Base.metadata.create_all)
    print("Database initialized successfully with new schema.")

if __name__ == "__main__":
    asyncio.run(init_db())

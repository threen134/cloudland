import pytest
import asyncio
from typing import AsyncGenerator
from httpx import AsyncClient
from sqlalchemy.ext.asyncio import AsyncSession, create_async_engine
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from app.main import app
from app.core.database import get_db, Base

# Use in-memory SQLite for testing (or a separate Postgres DB if preferred, but SQLite is easier for CI/agent)
# However, asyncpg doesn't work with sqlite. We need aiosqlite for that.
# Or we can just use the real config if the user has a DB running?
# The prompt says "My database is postgres". I might not have a running postgres instance here.
# So I should probably mock the DB or use `aiosqlite` for testing if I want to run it effectively.
# Let's add `aiosqlite` to requirements if possible, or try to rely on mocks.
# Actually, for this environment, let's use a mock for the DB session or just assume the code is correct and write the test that *would* run.
# But I need to Verify.
# Best approach: Mock the DB session dependency completely if I can't verify DB connection.
# Check if I can install `aiosqlite`.

# For now, let's try to mock the DB interaction using MagicMock if we can't run a real DB.
# But `sqlite+aiosqlite:///:memory:` is the standard way.

# Re-writing requirements to add aiosqlite for testing.

DATABASE_URL = "sqlite+aiosqlite:///:memory:"

engine = create_async_engine(
    DATABASE_URL, 
    connect_args={"check_same_thread": False}, 
    poolclass=StaticPool,
)

TestingSessionLocal = sessionmaker(
    engine, class_=AsyncSession, expire_on_commit=False
)

async def override_get_db():
    async with TestingSessionLocal() as session:
        yield session

app.dependency_overrides[get_db] = override_get_db

@pytest.fixture(scope="session")
def event_loop():
    loop = asyncio.get_event_loop_policy().new_event_loop()
    yield loop
    loop.close()

@pytest.fixture(scope="module")
async def client() -> AsyncGenerator:
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    
    async with AsyncClient(app=app, base_url="http://test") as ac:
        yield ac

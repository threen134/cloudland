from sqlalchemy.ext.asyncio import create_async_engine, AsyncSession
from sqlalchemy.orm import sessionmaker, declarative_base
from app.core.config import settings

"""
数据库基础设施配置
负责初始化 SQLAlchemy 异步引擎、会话工厂以及 FastAPI 依赖注入项。
"""

# 创建异步数据库引擎
engine = create_async_engine(settings.async_database_url, echo=True)

# 异步会话工厂
AsyncSessionLocal = sessionmaker(
    engine, class_=AsyncSession, expire_on_commit=False
)

# 声明式模型基类
Base = declarative_base()

async def get_db():
    """
    FastAPI 数据库会话依赖项
    为每个 HTTP 请求提供一个独立的异步数据库会话，并在请求结束时自动关闭。
    """
    async with AsyncSessionLocal() as session:
        yield session

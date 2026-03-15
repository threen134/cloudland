import logging
import sys
from app.core.config import settings

def setup_logging():
    """
    配置系统全局日志系统
    - 根据 settings.DEBUG 设置日志级别。
    - 同时输出到控制台(Stdout)和本地文件(app.log)。
    - 对 Uvicorn 和 SQLAlchemy 等第三方库进行差异化日志级别控制。
    """
    log_level = logging.DEBUG if settings.DEBUG else logging.INFO
    
    # Configure root logger
    logging.basicConfig(
        level=log_level,
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[
            logging.StreamHandler(sys.stdout)
        ]
    )
    
    # Set levels for our application logger
    logger.setLevel(log_level)
    
    # Set levels for some verbose libraries
    logging.getLogger("uvicorn").setLevel(logging.INFO if not settings.DEBUG else logging.DEBUG)
    logging.getLogger("sqlalchemy.engine").setLevel(logging.INFO if not settings.DEBUG else logging.DEBUG)
    logging.getLogger("aiosqlite").setLevel(logging.INFO if not settings.DEBUG else logging.DEBUG)
    logging.getLogger("httpx").setLevel(logging.INFO if not settings.DEBUG else logging.DEBUG)
    logging.getLogger("httpcore").setLevel(logging.INFO if not settings.DEBUG else logging.DEBUG)

# 导出中间件专用 Logger
logger = logging.getLogger("cloudland")

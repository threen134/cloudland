from sqlalchemy import Column, BigInteger, String, Boolean, DateTime, Text
from sqlalchemy.sql import func
from app.core.database import Base


class SystemSetting(Base):
    """
    全局系统设置。存储从环境变量迁移过来的系统参数，支持运行时修改、即时生效。
    value 统一使用 JSON 序列化存储，value_type 标识实际类型。
    is_secret=True 的字段 API 响应时自动脱敏，但不加密存储数据库。
    """
    __tablename__ = "system_settings"

    key = Column(String(128), primary_key=True, index=True)
    value = Column(Text, nullable=True)           # JSON 序列化的值
    value_type = Column(String(32), nullable=False)  # string | number | boolean | json | secret
    category = Column(String(32), nullable=False)    # general | quota | notification
    description = Column(String(256), nullable=True)
    is_secret = Column(Boolean, default=False, nullable=False)
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())


class SystemConfigVersion(Base):
    """
    全局配置版本号（单行记录，id 固定为 1）。
    每次 system_settings 发生变更时递增，用于多区域同步的乱序保护。
    """
    __tablename__ = "system_config_version"

    id = Column(BigInteger, primary_key=True, default=1)
    version = Column(BigInteger, nullable=False, default=0)
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

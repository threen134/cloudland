import uuid
from sqlalchemy import Column, Integer, String, Boolean, DateTime
from sqlalchemy.sql import func
from app.core.database import Base


class Region(Base):
    """
    云区域模型 (Region)
    代表一个独立的物理云环境（Cloudland 实例）。

    字段说明:
    - name: 区域唯一标识名 (如 'cn-north')，创建后不可修改。
    - internal_endpoint: Cloudland 内网地址，仅供 CPGateway Proxy 转发使用，不对前端暴露。
    - internal_secret: 每个 Region 独立的共享密钥，用于 CPGateway → Cloudland 的请求认证。
    - is_available: 是否对外可用，为 false 时不签发该 Region 的 Token。
    """
    __tablename__ = "regions"

    id = Column(Integer, primary_key=True, index=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    name = Column(String(64), unique=True, index=True, nullable=False)
    display_name = Column(String(128), nullable=True)
    internal_endpoint = Column(String(512), nullable=False)
    internal_secret = Column(String(256), nullable=False)
    is_available = Column(Boolean, default=True, nullable=False)
    maintenance_mode = Column(Boolean, default=False, nullable=False)
    description = Column(String, nullable=True)

    # 状态监控字段 (Status monitoring)
    last_check_at = Column(DateTime(timezone=True), nullable=True)
    status_message = Column(String(255), nullable=True)
    fail_count = Column(Integer, default=0, nullable=False)

    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

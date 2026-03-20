import uuid
from sqlalchemy import Column, BigInteger, String, Boolean, DateTime, Text
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.sql import func
from app.core.database import Base


class NotificationChannel(Base):
    """
    全局通知渠道模型。
    由用户在 CPGateway 侧创建/管理，变更后异步推送到各 Region 的 clapi 做镜像同步。
    Binding 关系不在此表存储，由各 Region 的 clapi 本地管理。
    """
    __tablename__ = "notification_channels"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    user_id = Column(BigInteger, index=True, nullable=False)
    name = Column(String(128), nullable=False)
    type = Column(String(32), nullable=False)  # feishu, webhook
    config = Column(JSONB, nullable=False)      # {"webhook_url": "...", "secret": "..."}
    enabled = Column(Boolean, default=True, nullable=False)
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

import uuid
from enum import IntEnum
from sqlalchemy import Column, BigInteger, Integer, String, DateTime, ForeignKey, Index
from sqlalchemy.sql import func
from sqlalchemy.orm import relationship
from app.core.database import Base


class InvitationStatus(IntEnum):
    PENDING = 0
    ACCEPTED = 1
    EXPIRED = 2
    CANCELLED = 3


class Invitation(Base):
    """
    组织邀请模型 (Invitation)
    记录向某个邮箱发出的组织邀请，支持已有用户和新用户两种场景。

    字段说明:
    - email: 被邀请人的邮箱地址
    - org_id: 邀请加入的组织 ID
    - org_role: 邀请加入后的角色 (1=Reader, 2=Writer, 3=Admin)
    - inviter_id: 发起邀请的用户 ID
    - token: 邀请令牌（邮件链接中使用）
    - status: 邀请状态 (0=Pending, 1=Accepted, 2=Expired, 3=Cancelled)
    - expires_at: 邀请过期时间
    """
    __tablename__ = "invitations"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    email = Column(String(255), nullable=False, index=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False, index=True)
    org_role = Column(Integer, default=1, nullable=False)  # OrgRole: 1=Reader
    inviter_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    token = Column(String(512), nullable=False, unique=True, index=True)
    status = Column(Integer, default=InvitationStatus.PENDING, nullable=False)

    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())
    expires_at = Column(DateTime(timezone=True), nullable=False)

    # Relationships
    organization = relationship("Organization")
    inviter = relationship("User")

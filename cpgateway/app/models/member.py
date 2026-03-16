import uuid
from enum import IntEnum
from sqlalchemy import Column, BigInteger, Integer, String, DateTime, ForeignKey, Index
from sqlalchemy.sql import func
from sqlalchemy.orm import relationship
from app.core.database import Base


class OrgRole(IntEnum):
    NONE = 0
    READER = 1
    WRITER = 2
    ADMIN = 3


class InvitationStatus(IntEnum):
    PENDING = 0
    ACCEPTED = 1
    EXPIRED = 2
    CANCELLED = 3


class Member(Base):
    """
    组织成员模型 (Member)
    关联 User 和 Organization，记录用户在某个 Org 中的角色。
    同时承载邀请功能：invitation_status 非 NULL 表示该记录来自邀请流程。

    字段说明:
    - org_role: 成员在 Org 中的角色 (0=None, 1=Reader, 2=Writer, 3=Admin)。
    - deleted_at: 软删除标记，非 NULL 表示已移除。
    - invitation_token: 邀请令牌（邮件链接中使用）。
    - invitation_status: 邀请状态 (0=Pending, 1=Accepted, 2=Expired, 3=Cancelled)。
    - invited_by: 发起邀请的用户 ID。
    - invitation_expires_at: 邀请过期时间。
    """
    __tablename__ = "members"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False, index=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False, index=True)
    org_role = Column(Integer, default=OrgRole.READER, nullable=False)

    # Invitation fields (NULL for directly added members)
    invitation_token = Column(String(512), nullable=True, unique=True, index=True)
    invitation_status = Column(Integer, nullable=True)
    invited_by = Column(BigInteger, ForeignKey("users.id"), nullable=True)
    invitation_expires_at = Column(DateTime(timezone=True), nullable=True)

    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())
    deleted_at = Column(DateTime(timezone=True), nullable=True)

    # Partial unique index: one active membership per user-org pair (deleted_at IS NULL)
    # Allows soft-deleted members to be re-added to the same org
    __table_args__ = (
        Index(
            'uq_member_user_org_active',
            'user_id', 'org_id',
            unique=True,
            postgresql_where=(Column('deleted_at').is_(None)),
        ),
    )

    # Relationships
    user = relationship("User", back_populates="members", foreign_keys=[user_id])
    organization = relationship("Organization", back_populates="members")
    inviter = relationship("User", foreign_keys=[invited_by])

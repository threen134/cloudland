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


class Member(Base):
    """
    组织成员模型 (Member)
    关联 User 和 Organization，记录用户在某个 Org 中的角色。

    字段说明:
    - org_role: 成员在 Org 中的角色 (0=None, 1=Reader, 2=Writer, 3=Admin)。
    - deleted_at: 软删除标记，非 NULL 表示已移除。
    """
    __tablename__ = "members"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False, index=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False, index=True)
    org_role = Column(Integer, default=OrgRole.READER, nullable=False)

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
    user = relationship("User", back_populates="members")
    organization = relationship("Organization", back_populates="members")

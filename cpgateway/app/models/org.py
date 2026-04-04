import uuid
from enum import IntEnum
from sqlalchemy import Column, Integer, BigInteger, String, DateTime, ForeignKey
from sqlalchemy.sql import func
from sqlalchemy.orm import relationship
from app.core.database import Base


class OrgType(IntEnum):
    TEAM = 1
    SYSTEM = 2


class OrgStatus(IntEnum):
    PENDING   = 0  # 待激活（用户注册后未激活账号）
    ACTIVE    = 1  # 正常运营
    SUSPENDED = 2  # 暂停（资源保留，禁止写操作）
    DISABLED  = 3  # 禁用（禁止登录和任何访问）


class Organization(Base):
    """
    全局组织模型 (Organization)
    Org 是全局概念，跨所有 Region 共享。资源通过 org_id 归属到 Org。

    字段说明:
    - slug: 唯一可读标识，创建后不可修改，用于 URL 友好引用。
    - org_type: TEAM(1) 普通团队, SYSTEM(2) 系统级 Org（全局唯一，不可删除）。
    - owner_user_id: Org 的 Owner，拥有最高管理权。
    - default_sg: 默认安全组 ID（由 Cloudland 创建后回填）。
    """
    __tablename__ = "organizations"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    name = Column(String(255), nullable=False)
    slug = Column(String(64), unique=True, index=True, nullable=False)
    org_type = Column(Integer, default=OrgType.TEAM, nullable=False)
    owner_user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    default_sg = Column(BigInteger, default=0)
    status = Column(Integer, default=OrgStatus.PENDING, nullable=False)

    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())
    deleted_at = Column(DateTime(timezone=True), nullable=True)

    # Relationships
    owner = relationship("User", foreign_keys=[owner_user_id])
    members = relationship("Member", back_populates="organization", cascade="all, delete-orphan")
    resource_quotas = relationship("OrgResourceQuota", back_populates="org", cascade="all, delete-orphan")
    resource_consumptions = relationship("OrgResourceConsumption", back_populates="org", cascade="all, delete-orphan")

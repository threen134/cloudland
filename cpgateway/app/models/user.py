import uuid
from enum import IntEnum
from sqlalchemy import Column, Integer, BigInteger, String, Boolean, DateTime
from sqlalchemy.orm import relationship
from sqlalchemy.sql import func
from app.core.database import Base


class SystemRole(IntEnum):
    USER = 0
    ADMIN = 1


class UserStatus(IntEnum):
    ACTIVE = 1
    DORMANT = 2
    DISABLED = 3


class User(Base):
    """
    全局用户模型 (User)
    CPGateway 是唯一的用户身份来源 (Single Source of Truth)。

    字段说明:
    - system_role: 系统级角色 (0=普通用户, 1=系统管理员)。
    - status: 用户状态 (1=Active, 2=Dormant 无 Org, 3=Disabled 被禁用)。
    - is_superuser: 保留兼容，等价于 system_role == ADMIN。
    """
    __tablename__ = "users"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    uuid = Column(String(36), unique=True, index=True, default=lambda: str(uuid.uuid4()))
    email = Column(String, unique=True, index=True, nullable=False)
    username = Column(String, unique=True, index=True, nullable=False)
    hashed_password = Column(String, nullable=False)
    first_name = Column(String(128), default='', nullable=False)
    last_name = Column(String(128), default='', nullable=False)
    language = Column(String(5), default="en", nullable=False)
    remark = Column(String(512), default='', nullable=False)
    system_role = Column(Integer, default=SystemRole.USER, nullable=False)
    status = Column(Integer, default=UserStatus.ACTIVE, nullable=False)
    is_active = Column(Boolean, default=False)
    is_superuser = Column(Boolean, default=False)
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    # Relationships
    resource_consumption = relationship("ResourceConsumption", back_populates="user", uselist=False, cascade="all, delete-orphan")
    resource_quota = relationship("ResourceQuota", back_populates="user", uselist=False, cascade="all, delete-orphan")
    members = relationship("Member", back_populates="user", cascade="all, delete-orphan")

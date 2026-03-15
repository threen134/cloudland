from sqlalchemy import Column, Integer, Float, ForeignKey, DateTime
from sqlalchemy.orm import relationship
from sqlalchemy.sql import func
from app.core.database import Base


class ResourceQuota(Base):
    """
    资源配额模型 (ResourceQuota)
    定义一个用户在全系统范围内允许持有的各类计算与网络资源上限。
    """
    __tablename__ = "resource_quota"

    id = Column(Integer, primary_key=True, index=True)
    user_id = Column(Integer, ForeignKey("users.id"), unique=True, nullable=False)
    
    # Quota limits
    max_cpu_cores = Column(Float, nullable=False)  # Maximum CPU cores allowed
    max_ram_gb = Column(Float, nullable=False)  # Maximum RAM in GB
    max_traffic_gb = Column(Float, nullable=False)  # Maximum network traffic in GB
    max_public_ips = Column(Integer, nullable=False)  # Maximum number of public IPs
    max_disk_gb = Column(Float, nullable=False)  # Maximum disk space in GB
    
    # Timestamps
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())
    
    # Relationship
    user = relationship("User", back_populates="resource_quota")

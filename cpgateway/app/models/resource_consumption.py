from sqlalchemy import Column, Integer, Float, ForeignKey, DateTime
from sqlalchemy.orm import relationship
from sqlalchemy.sql import func
from app.core.database import Base


class ResourceConsumption(Base):
    """
    资源消耗统计模型 (ResourceConsumption)
    实时记录（通常由异步任务或代理拦截器更新）用户当前已分配的资源总量。
    该数据用于与 `ResourceQuota` 进行比对，以实现资源超限拦截。
    """
    __tablename__ = "resource_consumption"

    id = Column(Integer, primary_key=True, index=True)
    user_id = Column(Integer, ForeignKey("users.id"), unique=True, nullable=False)
    
    # Resource consumption fields
    cpu_cores = Column(Float, default=0.0, nullable=False)  # Number of CPU cores
    ram_gb = Column(Float, default=0.0, nullable=False)  # RAM in GB
    traffic_gb = Column(Float, default=0.0, nullable=False)  # Network traffic in GB
    public_ips = Column(Integer, default=0, nullable=False)  # Number of public IP addresses
    disk_gb = Column(Float, default=0.0, nullable=False)  # Disk space in GB
    
    # Timestamps
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())
    
    # Relationship
    user = relationship("User", back_populates="resource_consumption")

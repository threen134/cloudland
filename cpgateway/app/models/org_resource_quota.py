from sqlalchemy import Column, BigInteger, Integer, Float, ForeignKey, DateTime, UniqueConstraint
from sqlalchemy.orm import relationship
from sqlalchemy.sql import func
from app.core.database import Base


class OrgResourceQuota(Base):
    """
    组织-区域级资源配额模型
    定义一个组织在特定 Region 内允许持有的各类计算与网络资源上限。
    唯一约束: (org_id, region_id) — 每个 org 在每个 region 只有一条配额记录。
    """
    __tablename__ = "org_resource_quotas"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False)
    region_id = Column(Integer, ForeignKey("regions.id"), nullable=False)

    # Quota limits
    max_cpu_cores = Column(Float, nullable=False, default=0.0)
    max_ram_gb = Column(Float, nullable=False, default=0.0)
    max_public_ips = Column(Integer, nullable=False, default=0)
    max_disk_gb = Column(Float, nullable=False, default=0.0)

    # Timestamps
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("org_id", "region_id", name="uq_org_region_quota"),
    )

    # Relationships
    org = relationship("Organization", back_populates="resource_quotas")
    region = relationship("Region")

from sqlalchemy import Column, BigInteger, Integer, Float, ForeignKey, DateTime, UniqueConstraint
from sqlalchemy.orm import relationship
from sqlalchemy.sql import func
from app.core.database import Base


class OrgResourceConsumption(Base):
    """
    组织-区域级资源消费模型
    实时记录一个组织在特定 Region 内已分配的资源总量。
    由 ProxyService 在 CREATE/DELETE/RESIZE 操作时自动更新。
    唯一约束: (org_id, region_id) — 每个 org 在每个 region 只有一条消费记录。
    """
    __tablename__ = "org_resource_consumptions"

    id = Column(BigInteger, primary_key=True, index=True, autoincrement=True)
    org_id = Column(BigInteger, ForeignKey("organizations.id"), nullable=False)
    region_id = Column(Integer, ForeignKey("regions.id"), nullable=False)

    # Resource consumption fields
    cpu_cores = Column(Float, default=0.0, nullable=False)
    ram_gb = Column(Float, default=0.0, nullable=False)
    public_ips = Column(Integer, default=0, nullable=False)
    disk_gb = Column(Float, default=0.0, nullable=False)

    # Timestamps
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now())

    __table_args__ = (
        UniqueConstraint("org_id", "region_id", name="uq_org_region_consumption"),
    )

    # Relationships
    org = relationship("Organization", back_populates="resource_consumptions")
    region = relationship("Region")

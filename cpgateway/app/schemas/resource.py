from pydantic import BaseModel, Field
from typing import Optional, List
from datetime import datetime


# === 嵌套用 schema（不含 org_uuid/region_name，由外层提供） ===

class QuotaFields(BaseModel):
    """配额字段（嵌套用）"""
    max_cpu_cores: float = Field(ge=0, description="Maximum CPU cores allowed")
    max_ram_gb: float = Field(ge=0, description="Maximum RAM in GB")
    max_public_ips: int = Field(ge=0, description="Maximum number of public IPs")
    max_disk_gb: float = Field(ge=0, description="Maximum disk space in GB")


class ConsumptionFields(BaseModel):
    """消费字段（嵌套用）"""
    cpu_cores: float = Field(ge=0, description="Number of CPU cores")
    ram_gb: float = Field(ge=0, description="RAM in GB")
    public_ips: int = Field(ge=0, description="Number of public IP addresses")
    disk_gb: float = Field(ge=0, description="Disk space in GB")


class OrgResourceQuotaUpdate(BaseModel):
    """配额更新请求"""
    max_cpu_cores: Optional[float] = Field(None, ge=0)
    max_ram_gb: Optional[float] = Field(None, ge=0)
    max_public_ips: Optional[int] = Field(None, ge=0)
    max_disk_gb: Optional[float] = Field(None, ge=0)


# === 独立返回用 schema（含完整标识信息） ===

class OrgResourceQuota(QuotaFields):
    """单独返回配额时使用"""
    org_uuid: str
    region_uuid: str
    region_name: str
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True


class OrgResourceConsumption(ConsumptionFields):
    """单独返回消费时使用"""
    org_uuid: str
    region_uuid: str
    region_name: str

    class Config:
        from_attributes = True


# === 组合 schema ===

class OrgResourceInfo(BaseModel):
    """单个 region 的配额和消费（嵌套用 Fields，避免重复字段）"""
    region_uuid: str
    region_name: str
    consumption: ConsumptionFields
    quota: QuotaFields


class OrgResourceSummary(BaseModel):
    """所有 region 的汇总"""
    org_uuid: str
    regions: List[OrgResourceInfo]

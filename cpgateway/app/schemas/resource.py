from pydantic import BaseModel, Field
from typing import Optional
from datetime import datetime


class ResourceConsumptionBase(BaseModel):
    """Base schema for resource consumption"""
    cpu_cores: float = Field(ge=0, description="Number of CPU cores")
    ram_gb: float = Field(ge=0, description="RAM in GB")
    traffic_gb: float = Field(ge=0, description="Network traffic in GB")
    public_ips: int = Field(ge=0, description="Number of public IP addresses")
    disk_gb: float = Field(ge=0, description="Disk space in GB")


class ResourceConsumptionCreate(ResourceConsumptionBase):
    """Schema for creating resource consumption record"""
    pass


class ResourceConsumptionUpdate(BaseModel):
    """Schema for updating resource consumption"""
    cpu_cores: Optional[float] = Field(None, ge=0)
    ram_gb: Optional[float] = Field(None, ge=0)
    traffic_gb: Optional[float] = Field(None, ge=0)
    public_ips: Optional[int] = Field(None, ge=0)
    disk_gb: Optional[float] = Field(None, ge=0)


class ResourceConsumption(ResourceConsumptionBase):
    """Schema for resource consumption response"""
    user_uuid: str
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True


# ===== Resource Quota Schemas =====

class ResourceQuotaBase(BaseModel):
    """Base schema for resource quota"""
    max_cpu_cores: float = Field(ge=0, description="Maximum CPU cores allowed")
    max_ram_gb: float = Field(ge=0, description="Maximum RAM in GB")
    max_traffic_gb: float = Field(ge=0, description="Maximum network traffic in GB")
    max_public_ips: int = Field(ge=0, description="Maximum number of public IPs")
    max_disk_gb: float = Field(ge=0, description="Maximum disk space in GB")


class ResourceQuotaCreate(ResourceQuotaBase):
    """Schema for creating resource quota"""
    pass


class ResourceQuotaUpdate(BaseModel):
    """Schema for updating resource quota"""
    max_cpu_cores: Optional[float] = Field(None, ge=0)
    max_ram_gb: Optional[float] = Field(None, ge=0)
    max_traffic_gb: Optional[float] = Field(None, ge=0)
    max_public_ips: Optional[int] = Field(None, ge=0)
    max_disk_gb: Optional[float] = Field(None, ge=0)


class ResourceQuota(ResourceQuotaBase):
    """Schema for resource quota response"""
    user_uuid: str
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True


# ===== Combined Response Schema =====

class UserResourceInfo(BaseModel):
    """Combined schema showing both consumption and quota"""
    consumption: ResourceConsumption
    quota: ResourceQuota

    class Config:
        from_attributes = True

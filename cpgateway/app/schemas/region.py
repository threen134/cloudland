from pydantic import BaseModel
from typing import Optional, List
from datetime import datetime


# --- Region Schemas ---

class RegionCreate(BaseModel):
    """创建 Region"""
    name: str
    display_name: Optional[str] = None
    internal_endpoint: str
    internal_secret: str = "auto-generate"
    description: Optional[str] = None


class RegionUpdate(BaseModel):
    """更新 Region"""
    display_name: Optional[str] = None
    internal_endpoint: Optional[str] = None
    is_available: Optional[bool] = None
    maintenance_mode: Optional[bool] = None
    description: Optional[str] = None


class RegionPublic(BaseModel):
    """公开的 Region 信息（不含内网地址和密钥）"""
    uuid: str
    name: str
    display_name: Optional[str] = None
    is_available: bool
    maintenance_mode: bool
    description: Optional[str] = None
    last_check_at: Optional[datetime] = None
    status_message: Optional[str] = None

    class Config:
        from_attributes = True


class RegionAdmin(RegionPublic):
    """管理员可见的 Region 信息（含内网地址，不含密钥）"""
    internal_endpoint: str
    fail_count: int
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True


class RegionCreated(RegionAdmin):
    """创建 Region 时的响应（包含 internal_secret，仅此一次）"""
    internal_secret: str

    class Config:
        from_attributes = True


class RegionSecretRotated(BaseModel):
    """密钥轮换响应"""
    region_uuid: str
    name: str
    new_secret: str

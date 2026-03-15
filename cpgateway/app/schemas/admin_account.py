from pydantic import BaseModel, Field
from typing import Optional
from datetime import datetime
from app.core.config import settings


class AdminAccountBase(BaseModel):
    """Base schema for admin account"""
    region: str = Field(..., description="Region identifier (e.g., us-east-1)")
    admin_account: str = Field(settings.DEFAULT_ADMIN_USERNAME, description=f"Admin username, default '{settings.DEFAULT_ADMIN_USERNAME}'")
    email: Optional[str] = Field(None, description="Admin email address")


class AdminAccountCreate(AdminAccountBase):
    """Schema for creating admin account"""
    password: str = Field(..., min_length=8, description="Admin password")


class AdminAccountUpdate(BaseModel):
    """Schema for updating admin account"""
    password: str = Field(..., min_length=8, description="New password")
    

class AdminAccount(AdminAccountBase):
    """Schema for admin account response (password excluded)"""
    id: int
    uuid: str
    region_id: Optional[int] = Field(None, description="Internal ID of the associated region")
    region_uuid: Optional[str] = Field(None, description="External UUID of the associated region")
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True

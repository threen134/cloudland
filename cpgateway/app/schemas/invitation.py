from pydantic import BaseModel, EmailStr
from typing import Optional
from datetime import datetime


class InvitationCreate(BaseModel):
    """创建邀请请求"""
    email: EmailStr
    org_role: int = 1  # default Reader
    is_superuser: bool = False  # only for SYSTEM org


class InvitationResponse(BaseModel):
    """邀请响应"""
    uuid: str
    email: str
    org_uuid: str
    org_name: str
    org_role: int
    status: int
    inviter_email: str
    created_at: datetime
    expires_at: datetime

    class Config:
        from_attributes = True


class InvitationAccept(BaseModel):
    """接受邀请请求（新用户需要设置密码和用户名）"""
    token: str
    username: Optional[str] = None
    password: Optional[str] = None


class InvitationInfo(BaseModel):
    """邀请信息（接受页面展示用）"""
    email: str
    org_name: str
    org_role: int
    inviter_email: str
    is_existing_user: bool
    expires_at: datetime

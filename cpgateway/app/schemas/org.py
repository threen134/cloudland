from pydantic import BaseModel
from typing import Optional, List
from datetime import datetime


class OrgCreate(BaseModel):
    name: str
    slug: str


class OrgUpdate(BaseModel):
    name: Optional[str] = None


class OrgResponse(BaseModel):
    uuid: str
    name: str
    slug: str
    org_type: int
    owner_uuid: str
    created_at: datetime

    class Config:
        from_attributes = True


class OrgDetail(OrgResponse):
    member_count: int = 0
    owner_email: Optional[str] = None


class MemberAdd(BaseModel):
    user_uuid: str
    org_role: int = 1   # default Reader


class MemberUpdate(BaseModel):
    org_role: int


class MemberResponse(BaseModel):
    uuid: str
    user_uuid: str
    org_uuid: str
    org_role: int
    user_email: Optional[str] = None
    is_owner: bool = False
    invitation_status: Optional[int] = None
    created_at: datetime

    class Config:
        from_attributes = True


class TransferOwner(BaseModel):
    new_owner_uuid: str


class UserOrgItem(BaseModel):
    """用户所属 Org 列表项（供前端切换 Org 下拉框使用）"""
    uuid: str
    name: str
    slug: str
    org_type: int = 1
    org_role: int
    is_owner: bool
    is_current: bool = False

    class Config:
        from_attributes = True

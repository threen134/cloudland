from pydantic import BaseModel
from typing import Optional, List
from datetime import datetime


class OrgCreate(BaseModel):
    name: str
    slug: str


class OrgUpdate(BaseModel):
    name: Optional[str] = None


class OrgResponse(BaseModel):
    id: int
    uuid: str
    name: str
    slug: str
    org_type: int
    owner_user_id: int
    created_at: datetime

    class Config:
        from_attributes = True


class OrgDetail(OrgResponse):
    member_count: int = 0
    owner_email: Optional[str] = None


class MemberAdd(BaseModel):
    user_id: int
    org_role: int = 1   # default Reader


class MemberUpdate(BaseModel):
    org_role: int


class MemberResponse(BaseModel):
    id: int
    uuid: str
    user_id: int
    org_id: int
    org_role: int
    user_email: Optional[str] = None
    created_at: datetime

    class Config:
        from_attributes = True


class TransferOwner(BaseModel):
    new_owner_user_id: int


class UserOrgItem(BaseModel):
    """用户所属 Org 列表项（供前端切换 Org 下拉框使用）"""
    org_id: int
    name: str
    slug: str
    org_role: int
    is_owner: bool
    is_current: bool = False

    class Config:
        from_attributes = True

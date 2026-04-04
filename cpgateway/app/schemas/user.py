from pydantic import BaseModel, EmailStr, Field, field_validator
from datetime import datetime
from typing import Optional, Literal, List
from app.models.user import UserStatus


class UserBase(BaseModel):
    """用户基础模型 (Pydantic)"""
    email: EmailStr
    username: str
    language: str = "en"


class UserCreate(UserBase):
    """用户注册/创建模型"""
    password: str
    org_name: str = Field(..., min_length=2, max_length=128, description="Organization name")
    org_slug: str = Field(..., min_length=2, max_length=64, pattern=r'^[a-z0-9][a-z0-9-]*[a-z0-9]$', description="Organization slug (lowercase, alphanumeric, hyphens)")


class UserUpdate(UserBase):
    """用户信息更新模型 (密码可选)"""
    password: Optional[str] = None


class UserInDBBase(UserBase):
    """数据库实体对应的 Pydantic 模型基类"""
    uuid: str
    is_active: bool
    is_superuser: bool = False
    system_role: int = 0
    status: str = "active"
    first_name: str = ""
    last_name: str = ""
    remark: str = ""
    created_at: datetime

    @field_validator('status', mode='before')
    @classmethod
    def convert_status(cls, v):
        if isinstance(v, int):
            try:
                return UserStatus(v).name.lower()
            except ValueError:
                return str(v)
        return v

    class Config:
        from_attributes = True


class User(UserInDBBase):
    """完整的用户信息模型"""
    pass


class UserRegisterResponse(BaseModel):
    """用户注册响应模型"""
    message: str
    user: User


class UserWithToken(User):
    """带访问令牌的用户响应 (登录后返回)"""
    access_token: str
    token_type: str = "bearer"


class Msg(BaseModel):
    """标准操作响应消息"""
    message: str
    result: bool = True
    status: str


class UserProfileUpdate(BaseModel):
    """Profile 更新请求"""
    first_name: Optional[str] = None
    last_name: Optional[str] = None
    language: Optional[str] = None
    remark: Optional[str] = None


class PasswordChange(BaseModel):
    """密码修改请求"""
    old_password: str
    new_password: str = Field(..., min_length=8, max_length=64)

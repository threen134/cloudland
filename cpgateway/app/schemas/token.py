from pydantic import BaseModel
from typing import Optional


class Token(BaseModel):
    access_token: str
    token_type: str


class TokenWithContext(BaseModel):
    access_token: str
    token_type: str = "bearer"
    expires_in: int
    org_uuid: Optional[str] = None
    org_name: Optional[str] = None
    region: Optional[str] = None


class TokenData(BaseModel):
    username: Optional[str] = None


class LoginRequest(BaseModel):
    username: str
    password: str
    org_uuid: Optional[str] = None
    region: Optional[str] = None


class SwitchOrgRequest(BaseModel):
    org_uuid: str
    region: Optional[str] = None


class SwitchRegionRequest(BaseModel):
    region: str

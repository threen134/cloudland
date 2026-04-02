import json
from pydantic import BaseModel, model_validator
from typing import Optional, List, Any
from datetime import datetime

MASKED = "******"


class SettingResponse(BaseModel):
    """单条系统设置响应（secret 字段自动脱敏）"""
    key: str
    value: Any           # 已反序列化的值（前端直接使用）
    value_type: str
    category: str
    description: Optional[str] = None
    is_secret: bool
    updated_at: Optional[datetime] = None

    class Config:
        from_attributes = True

    @model_validator(mode="after")
    def deserialize_and_mask(self) -> "SettingResponse":
        # 反序列化 JSON value
        if isinstance(self.value, str):
            try:
                self.value = json.loads(self.value)
            except (json.JSONDecodeError, TypeError):
                pass
        # 脱敏 secret 字段
        if self.is_secret and self.value:
            self.value = MASKED
        return self


class SettingsListResponse(BaseModel):
    """系统设置列表响应"""
    settings: List[SettingResponse]


class TestNotificationRequest(BaseModel):
    """测试通知渠道连通性"""
    channel: str  # email | feishu | slack | webhook


class TestNotificationResponse(BaseModel):
    """测试通知结果"""
    channel: str
    success: bool
    message: str



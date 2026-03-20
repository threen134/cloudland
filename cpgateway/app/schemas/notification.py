import ipaddress
from urllib.parse import urlparse

from pydantic import BaseModel, field_validator, model_validator
from typing import Optional, List, Dict, Any
from datetime import datetime


def _validate_webhook_url(url: str, field_name: str = "url") -> None:
    """校验 webhook URL：必须为 https，禁止内网地址（防 SSRF）"""
    parsed = urlparse(url)
    if parsed.scheme not in ("https", "http"):
        raise ValueError(f"{field_name} must use http or https scheme")
    hostname = parsed.hostname
    if not hostname:
        raise ValueError(f"{field_name} has no valid hostname")
    try:
        addr = ipaddress.ip_address(hostname)
        if addr.is_private or addr.is_loopback or addr.is_link_local or addr.is_reserved:
            raise ValueError(f"{field_name} must not point to a private/internal address")
    except ValueError as e:
        if "must not point" in str(e):
            raise
        # hostname is a domain name, not an IP — allow it


class ChannelCreate(BaseModel):
    """创建通知渠道"""
    name: str
    type: str  # feishu, webhook
    config: Dict[str, Any]
    enabled: bool = True

    @field_validator("type")
    @classmethod
    def validate_type(cls, v: str) -> str:
        if v not in ("feishu", "webhook"):
            raise ValueError("type must be 'feishu' or 'webhook'")
        return v

    @model_validator(mode="after")
    def validate_config(self) -> "ChannelCreate":
        """根据渠道类型校验 config 必需字段及 URL 安全性"""
        if self.type == "feishu":
            url = self.config.get("webhook_url")
            if not url or not isinstance(url, str):
                raise ValueError("feishu channel requires 'webhook_url' in config")
            _validate_webhook_url(url, "webhook_url")
        elif self.type == "webhook":
            url = self.config.get("url")
            if not url or not isinstance(url, str):
                raise ValueError("webhook channel requires 'url' in config")
            _validate_webhook_url(url, "url")
        return self


class ChannelUpdate(BaseModel):
    """更新通知渠道"""
    name: Optional[str] = None
    config: Optional[Dict[str, Any]] = None
    enabled: Optional[bool] = None


class ChannelResponse(BaseModel):
    """通知渠道响应（secret 字段脱敏）"""
    uuid: str
    name: str
    type: str
    config: Dict[str, Any]
    enabled: bool
    created_at: datetime
    updated_at: datetime

    class Config:
        from_attributes = True

    @model_validator(mode="after")
    def mask_secrets(self) -> "ChannelResponse":
        """脱敏 config 中的 secret 字段，避免前端泄露签名密钥"""
        if self.config and "secret" in self.config and self.config["secret"]:
            self.config = {**self.config, "secret": "******"}
        return self


class ChannelListResponse(BaseModel):
    """通知渠道列表响应"""
    total: int
    channels: List[ChannelResponse]


class ChannelSyncPayload(BaseModel):
    """推送到 clapi 的同步 payload"""
    action: str  # upsert, delete
    channel: Optional[Dict[str, Any]] = None
    channel_uuid: Optional[str] = None  # for delete action


class ChannelBulkSyncPayload(BaseModel):
    """全量推送到 clapi 的同步 payload"""
    action: str = "bulk_sync"
    channels: List[Dict[str, Any]]


class AlarmSummaryRegion(BaseModel):
    """单个区域的告警汇总"""
    region_uuid: str
    region_name: str
    firing_count: int


class AlarmSummaryResponse(BaseModel):
    """全局告警汇总响应"""
    regions: List[AlarmSummaryRegion]
    total_firing: int

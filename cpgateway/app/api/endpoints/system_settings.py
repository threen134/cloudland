"""
系统设置管理接口（仅 SuperAdmin）。
GET /api/v1/system/settings       — 获取全部设置（secret 字段脱敏）
PUT /api/v1/system/settings       — 批量更新（跳过值为 "******" 的 secret 字段）
POST /api/v1/system/settings/test-notification — 测试指定渠道连通性
"""
import json
import smtplib
import email.mime.text
import ssl
import asyncio
import hmac as hmac_lib
import hashlib
import base64
import time

import httpx
from fastapi import APIRouter, Body, Depends, HTTPException, status, BackgroundTasks
from sqlalchemy.ext.asyncio import AsyncSession
from typing import Any, Dict, List

from app.core.database import get_db, AsyncSessionLocal
from app.api.deps import get_current_superuser
from app.models.user import User
from app.schemas.system_setting import (
    SettingResponse,
    SettingsListResponse,
    TestNotificationRequest,
    TestNotificationResponse,
    MASKED,
)
from app.services.settings_service import settings_service, SETTINGS_METADATA, _deserialize
from app.core.logging_config import logger

router = APIRouter()


@router.get("", response_model=SettingsListResponse)
async def list_settings(
    db: AsyncSession = Depends(get_db),
    _: User = Depends(get_current_superuser),
):
    """获取所有系统设置（含元数据默认值补全，secret 字段脱敏）"""
    rows = await settings_service.get_all(db)
    return SettingsListResponse(
        settings=[SettingResponse.model_validate(r) for r in rows]
    )


@router.put("", response_model=SettingsListResponse)
async def update_settings(
    background_tasks: BackgroundTasks,
    payload: Dict[str, Any] = Body(..., description="{ setting_key: new_value, ... }"),
    db: AsyncSession = Depends(get_db),
    _: User = Depends(get_current_superuser),
):
    """
    批量更新系统设置。
    payload: { "SMTP_HOST": "mail.example.com", "SMTP_PORT": 587, ... }
    secret 字段若前端回传 "******"，服务端自动跳过，不覆盖真实值。
    """
    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="Payload must be a JSON object")

    # 过滤不在已知 key 列表中的字段
    valid_updates = {k: v for k, v in payload.items() if k in SETTINGS_METADATA}
    if not valid_updates:
        raise HTTPException(status_code=400, detail="No valid settings keys provided")

    _, version = await settings_service.bulk_update(db, valid_updates)
    await db.commit()

    logger.info(f"System settings updated: {list(valid_updates.keys())}, config_version={version}")

    # 异步推送到所有 Region
    background_tasks.add_task(_push_settings_to_all_regions)

    rows = await settings_service.get_all(db)
    return SettingsListResponse(
        settings=[SettingResponse.model_validate(r) for r in rows]
    )


@router.post("/test-notification", response_model=TestNotificationResponse)
async def test_notification(
    req: TestNotificationRequest,
    db: AsyncSession = Depends(get_db),
    _: User = Depends(get_current_superuser),
):
    """测试通知渠道连通性"""
    channel = req.channel
    try:
        if channel == "email":
            result = await _test_email(db)
        elif channel == "feishu":
            result = await _test_feishu(db)
        elif channel == "slack":
            result = await _test_slack(db)
        elif channel == "webhook":
            result = await _test_webhook(db)
        else:
            raise HTTPException(status_code=400, detail=f"Unsupported channel: {channel}")
    except HTTPException:
        raise
    except Exception as e:
        return TestNotificationResponse(channel=channel, success=False, message=str(e))

    return result


# --- Helpers ---

async def _push_settings_to_all_regions():
    """后台任务：将系统设置同步到所有活跃 Region"""
    from app.services.settings_sync_service import settings_sync_service
    async with AsyncSessionLocal() as db:
        try:
            await settings_sync_service.push_settings_to_all_regions(db)
        except Exception as e:
            logger.error(f"Failed to push system settings to regions: {e}")


async def _test_email(db: AsyncSession) -> TestNotificationResponse:
    host = await settings_service.get(db, "SMTP_HOST") or ""
    port = int(await settings_service.get(db, "SMTP_PORT") or 587)
    tls = await settings_service.get(db, "SMTP_TLS")
    user = await settings_service.get(db, "SMTP_USER") or ""
    password = await settings_service.get(db, "SMTP_PASSWORD") or ""
    from_addr = await settings_service.get(db, "SMTP_FROM") or ""
    from_name = await settings_service.get(db, "SMTP_FROM_NAME") or "CloudLand"

    if not host:
        return TestNotificationResponse(channel="email", success=False, message="SMTP_HOST not configured")

    def _send():
        msg = email.mime.text.MIMEText("This is a test notification from CloudLand.", "plain", "utf-8")
        msg["Subject"] = f"[{from_name}] Test Notification"
        msg["From"] = f"{from_name} <{from_addr}>"
        msg["To"] = from_addr

        if port == 465:
            # 隐式 SSL（SMTP_SSL）
            context = ssl.create_default_context()
            with smtplib.SMTP_SSL(host, port, context=context) as smtp:
                if user:
                    smtp.login(user, password)
                smtp.send_message(msg)
        elif tls:
            # 显式 TLS（STARTTLS，常见于端口 587）
            with smtplib.SMTP(host, port) as smtp:
                smtp.ehlo()
                smtp.starttls(context=ssl.create_default_context())
                if user:
                    smtp.login(user, password)
                smtp.send_message(msg)
        else:
            # 明文 SMTP（如内网端口 25）
            with smtplib.SMTP(host, port) as smtp:
                smtp.ehlo()
                if user:
                    smtp.login(user, password)
                smtp.send_message(msg)

    try:
        await asyncio.get_running_loop().run_in_executor(None, _send)
        return TestNotificationResponse(channel="email", success=True, message="Test email sent successfully")
    except Exception as e:
        return TestNotificationResponse(channel="email", success=False, message=str(e))


async def _test_feishu(db: AsyncSession) -> TestNotificationResponse:
    webhook_url = await settings_service.get(db, "FEISHU_WEBHOOK_URL") or ""
    feishu_secret = await settings_service.get(db, "FEISHU_SECRET") or ""
    if not webhook_url:
        return TestNotificationResponse(channel="feishu", success=False, message="FEISHU_WEBHOOK_URL not configured")

    payload: dict = {
        "msg_type": "text",
        "content": {"text": "[CloudLand] Test notification — 飞书渠道连通性测试成功"},
    }

    # 若配置了签名密钥，使用与 notifier.go 相同的 HMAC-SHA256 签名算法进行测试
    if feishu_secret:
        timestamp = str(int(time.time()))
        string_to_sign = f"{timestamp}\n{feishu_secret}"
        mac = hmac_lib.new(string_to_sign.encode("utf-8"), digestmod=hashlib.sha256)
        sign = base64.b64encode(mac.digest()).decode("utf-8")
        payload["timestamp"] = timestamp
        payload["sign"] = sign

    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(webhook_url, json=payload)
        if resp.status_code == 200:
            data = resp.json()
            if data.get("code") == 0 or data.get("StatusCode") == 0:
                return TestNotificationResponse(channel="feishu", success=True, message="Feishu webhook OK")
            return TestNotificationResponse(channel="feishu", success=False, message=f"Feishu returned: {data}")
        return TestNotificationResponse(channel="feishu", success=False, message=f"HTTP {resp.status_code}")


async def _test_slack(db: AsyncSession) -> TestNotificationResponse:
    webhook_url = await settings_service.get(db, "SLACK_WEBHOOK_URL") or ""
    if not webhook_url:
        return TestNotificationResponse(channel="slack", success=False, message="SLACK_WEBHOOK_URL not configured")

    payload = {
        "text": "[CloudLand] Test notification — Slack channel connectivity test successful",
    }
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(webhook_url, json=payload)
        if resp.status_code == 200:
            return TestNotificationResponse(channel="slack", success=True, message="Slack webhook OK")
        return TestNotificationResponse(channel="slack", success=False, message=f"HTTP {resp.status_code}: {resp.text[:200]}")


async def _test_webhook(db: AsyncSession) -> TestNotificationResponse:
    url = await settings_service.get(db, "CUSTOM_WEBHOOK_URL") or ""
    method = (await settings_service.get(db, "CUSTOM_WEBHOOK_METHOD") or "POST").upper()
    headers_raw = await settings_service.get(db, "CUSTOM_WEBHOOK_HEADERS") or {}
    # headers 可能以 JSON 字符串形式存储，确保解析为 dict
    if isinstance(headers_raw, str):
        try:
            headers_raw = json.loads(headers_raw)
        except Exception:
            headers_raw = {}
    if not isinstance(headers_raw, dict):
        headers_raw = {}

    if not url:
        return TestNotificationResponse(channel="webhook", success=False, message="CUSTOM_WEBHOOK_URL not configured")

    # 仅允许合法 HTTP 方法，防止注入
    allowed_methods = {"GET", "POST", "PUT", "PATCH"}
    if method not in allowed_methods:
        return TestNotificationResponse(channel="webhook", success=False, message=f"Unsupported HTTP method: {method}")

    payload = {"event": "test", "message": "CloudLand webhook connectivity test"}
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.request(method, url, json=payload, headers=headers_raw)
        if resp.status_code < 300:
            return TestNotificationResponse(channel="webhook", success=True, message=f"Webhook OK (HTTP {resp.status_code})")
        return TestNotificationResponse(channel="webhook", success=False, message=f"HTTP {resp.status_code}: {resp.text[:200]}")

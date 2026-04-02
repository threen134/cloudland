"""
统一通知模块 - 支持邮件 (SMTP) 和飞书机器人 (Feishu Bot) 两种方式。

通过动态设置 NOTIFICATION_CHANNELS 配置启用的渠道列表（如 ["email", "feishu"]）。
"""

import time
import hmac
import hashlib
import base64
import httpx
import logging
from app.core.email import send_activation_email as _send_email
from app.core.email import send_invitation_email as _send_invitation_email

logger = logging.getLogger(__name__)


async def _get_notification_config() -> dict:
    """从动态设置获取通知相关配置"""
    from app.core.database import AsyncSessionLocal
    from app.services.settings_service import settings_service

    async with AsyncSessionLocal() as db:
        channels_raw = await settings_service.get(db, "NOTIFICATION_CHANNELS")
        channels = channels_raw if isinstance(channels_raw, list) else ["email"]
        return {
            "channels": channels,
            "feishu_webhook_url": await settings_service.get(db, "FEISHU_WEBHOOK_URL") or "",
            "feishu_secret": await settings_service.get(db, "FEISHU_SECRET") or "",
            "frontend_url": await settings_service.get(db, "FRONTEND_URL") or "",
        }


def _gen_feishu_sign(secret: str, timestamp: int) -> str:
    """生成飞书 Webhook 签名 (HmacSHA256)"""
    string_to_sign = f"{timestamp}\n{secret}"
    hmac_code = hmac.new(string_to_sign.encode("utf-8"), digestmod=hashlib.sha256).digest()
    return base64.b64encode(hmac_code).decode("utf-8")


async def _send_feishu(
    config: dict, email: str, username: str, token: str, language: str = "en",
) -> bool:
    """通过飞书机器人 Webhook 发送激活通知"""
    webhook_url = config["feishu_webhook_url"]
    if not webhook_url:
        logger.warning("FEISHU_WEBHOOK_URL not configured, skipping feishu notification")
        return False

    activation_link = f"{config['frontend_url']}/activate?token={token}"

    if language == "zh":
        title = "CloudLand - 新用户注册激活"
        content_lines = [
            [{"tag": "text", "text": f"用户 {username} ({email}) 已注册，请激活账户："}],
            [{"tag": "a", "text": "点击激活账户", "href": activation_link}],
            [{"tag": "text", "text": f"激活令牌: {token}"}],
            [{"tag": "text", "text": "此链接 24 小时内有效。"}],
        ]
        lang_key = "zh_cn"
    else:
        title = "CloudLand - New User Activation"
        content_lines = [
            [{"tag": "text", "text": f"User {username} ({email}) has registered. Please activate:"}],
            [{"tag": "a", "text": "Click to Activate Account", "href": activation_link}],
            [{"tag": "text", "text": f"Activation Token: {token}"}],
            [{"tag": "text", "text": "This link expires in 24 hours."}],
        ]
        lang_key = "en_us"

    payload = {
        "msg_type": "post",
        "content": {
            "post": {
                lang_key: {
                    "title": title,
                    "content": content_lines,
                }
            }
        },
    }

    # 如果配置了签名密钥，添加签名
    if config["feishu_secret"]:
        timestamp = int(time.time())
        sign = _gen_feishu_sign(config["feishu_secret"], timestamp)
        payload["timestamp"] = str(timestamp)
        payload["sign"] = sign

    try:
        async with httpx.AsyncClient() as client:
            resp = await client.post(webhook_url, json=payload, timeout=10)
            result = resp.json()
            if result.get("code") == 0 or result.get("StatusCode") == 0:
                logger.info(f"Feishu notification sent for user {username} ({email})")
                return True
            else:
                logger.error(f"Feishu API error: {result}")
                return False
    except Exception as e:
        logger.error(f"Failed to send feishu notification for {email}: {e}")
        return False


async def send_activation_notification(email: str, username: str, token: str, language: str = "en") -> bool:
    """
    根据动态设置 NOTIFICATION_CHANNELS 发送激活通知。

    Args:
        email: 用户邮箱
        username: 用户名
        token: 激活令牌
        language: 语言偏好 ('en' or 'zh')

    Returns:
        True if at least one notification method succeeded
    """
    config = await _get_notification_config()
    channels = config["channels"]
    results = []

    if "email" in channels:
        ok = await _send_email(email, username, token, language)
        results.append(ok)

    if "feishu" in channels:
        ok = await _send_feishu(config, email, username, token, language)
        results.append(ok)

    if not results:
        logger.warning(f"No valid notification channels configured, falling back to email")
        ok = await _send_email(email, username, token, language)
        results.append(ok)

    return any(results)


async def send_invitation_notification(
    email: str, org_name: str, inviter_name: str, token: str,
    is_existing_user: bool = False, language: str = "en",
) -> bool:
    """
    根据动态设置 NOTIFICATION_CHANNELS 发送邀请通知。
    """
    config = await _get_notification_config()
    channels = config["channels"]
    results = []

    if "email" in channels:
        ok = await _send_invitation_email(
            email, org_name, inviter_name, token,
            is_existing_user=is_existing_user, language=language,
        )
        results.append(ok)

    if "feishu" in channels:
        ok = await _send_feishu_invitation(config, email, org_name, inviter_name, token, language)
        results.append(ok)

    if not results:
        logger.warning(f"No valid notification channels configured, falling back to email")
        ok = await _send_invitation_email(
            email, org_name, inviter_name, token,
            is_existing_user=is_existing_user, language=language,
        )
        results.append(ok)

    return any(results)


async def _send_feishu_invitation(
    config: dict, email: str, org_name: str, inviter_name: str, token: str, language: str = "en",
) -> bool:
    """通过飞书机器人 Webhook 发送邀请通知"""
    webhook_url = config["feishu_webhook_url"]
    if not webhook_url:
        logger.warning("FEISHU_WEBHOOK_URL not configured, skipping feishu invitation notification")
        return False

    accept_link = f"{config['frontend_url']}/invite/accept?token={token}"

    if language == "zh":
        title = f"CloudLand - 组织邀请: {org_name}"
        content_lines = [
            [{"tag": "text", "text": f"{inviter_name} 邀请 {email} 加入组织 {org_name}"}],
            [{"tag": "a", "text": "接受邀请", "href": accept_link}],
            [{"tag": "text", "text": "此链接 24 小时内有效。"}],
        ]
        lang_key = "zh_cn"
    else:
        title = f"CloudLand - Org Invitation: {org_name}"
        content_lines = [
            [{"tag": "text", "text": f"{inviter_name} invited {email} to join {org_name}"}],
            [{"tag": "a", "text": "Accept Invitation", "href": accept_link}],
            [{"tag": "text", "text": "This link expires in 24 hours."}],
        ]
        lang_key = "en_us"

    payload = {
        "msg_type": "post",
        "content": {
            "post": {
                lang_key: {
                    "title": title,
                    "content": content_lines,
                }
            }
        },
    }

    if config["feishu_secret"]:
        timestamp = int(time.time())
        sign = _gen_feishu_sign(config["feishu_secret"], timestamp)
        payload["timestamp"] = str(timestamp)
        payload["sign"] = sign

    try:
        async with httpx.AsyncClient() as client:
            resp = await client.post(webhook_url, json=payload, timeout=10)
            result = resp.json()
            if result.get("code") == 0 or result.get("StatusCode") == 0:
                logger.info(f"Feishu invitation notification sent for {email} -> {org_name}")
                return True
            else:
                logger.error(f"Feishu API error: {result}")
                return False
    except Exception as e:
        logger.error(f"Failed to send feishu invitation notification for {email}: {e}")
        return False

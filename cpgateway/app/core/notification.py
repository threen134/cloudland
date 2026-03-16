"""
统一通知模块 - 支持邮件 (SMTP) 和飞书机器人 (Feishu Bot) 两种方式。

通过 settings.NOTIFICATION_METHOD 配置:
  - "email"  : 仅发送邮件
  - "feishu" : 仅发送飞书机器人消息
  - "both"   : 同时发送
"""

import time
import hmac
import hashlib
import base64
import httpx
import logging
from app.core.config import settings
from app.core.email import send_activation_email as _send_email
from app.core.email import send_invitation_email as _send_invitation_email

logger = logging.getLogger(__name__)


def _gen_feishu_sign(secret: str, timestamp: int) -> str:
    """生成飞书 Webhook 签名 (HmacSHA256)"""
    string_to_sign = f"{timestamp}\n{secret}"
    hmac_code = hmac.new(string_to_sign.encode("utf-8"), digestmod=hashlib.sha256).digest()
    return base64.b64encode(hmac_code).decode("utf-8")


async def _send_feishu(email: str, username: str, token: str, language: str = "en") -> bool:
    """通过飞书机器人 Webhook 发送激活通知"""
    webhook_url = settings.FEISHU_WEBHOOK_URL
    if not webhook_url:
        logger.warning("FEISHU_WEBHOOK_URL not configured, skipping feishu notification")
        return False

    activation_link = f"{settings.FRONTEND_URL}/activate?token={token}"

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
    if settings.FEISHU_SECRET:
        timestamp = int(time.time())
        sign = _gen_feishu_sign(settings.FEISHU_SECRET, timestamp)
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
    根据 NOTIFICATION_METHOD 配置发送激活通知。

    Args:
        email: 用户邮箱
        username: 用户名
        token: 激活令牌
        language: 语言偏好 ('en' or 'zh')

    Returns:
        True if at least one notification method succeeded
    """
    method = settings.NOTIFICATION_METHOD.lower().strip()
    results = []

    if method in ("email", "both"):
        ok = await _send_email(email, username, token, language)
        results.append(ok)

    if method in ("feishu", "both"):
        ok = await _send_feishu(email, username, token, language)
        results.append(ok)

    if not results:
        logger.warning(f"NOTIFICATION_METHOD='{method}' is invalid, falling back to email")
        ok = await _send_email(email, username, token, language)
        results.append(ok)

    return any(results)


async def send_invitation_notification(
    email: str, org_name: str, inviter_name: str, token: str,
    is_existing_user: bool = False, language: str = "en",
) -> bool:
    """
    根据 NOTIFICATION_METHOD 配置发送邀请通知。
    """
    method = settings.NOTIFICATION_METHOD.lower().strip()
    results = []

    if method in ("email", "both"):
        ok = await _send_invitation_email(
            email, org_name, inviter_name, token,
            is_existing_user=is_existing_user, language=language,
        )
        results.append(ok)

    if method in ("feishu", "both"):
        ok = await _send_feishu_invitation(email, org_name, inviter_name, token, language)
        results.append(ok)

    if not results:
        logger.warning(f"NOTIFICATION_METHOD='{method}' is invalid, falling back to email")
        ok = await _send_invitation_email(
            email, org_name, inviter_name, token,
            is_existing_user=is_existing_user, language=language,
        )
        results.append(ok)

    return any(results)


async def _send_feishu_invitation(
    email: str, org_name: str, inviter_name: str, token: str, language: str = "en",
) -> bool:
    """通过飞书机器人 Webhook 发送邀请通知"""
    webhook_url = settings.FEISHU_WEBHOOK_URL
    if not webhook_url:
        logger.warning("FEISHU_WEBHOOK_URL not configured, skipping feishu invitation notification")
        return False

    accept_link = f"{settings.FRONTEND_URL}/invite/accept?token={token}"

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

    if settings.FEISHU_SECRET:
        timestamp = int(time.time())
        sign = _gen_feishu_sign(settings.FEISHU_SECRET, timestamp)
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

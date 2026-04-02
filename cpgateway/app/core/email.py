import aiosmtplib
from email.mime.text import MIMEText
from email.mime.multipart import MIMEMultipart
import logging

logger = logging.getLogger(__name__)


async def _get_email_config() -> dict:
    """从动态设置获取邮件相关配置（三级降级：DB → 环境变量 → 默认值）"""
    from app.core.database import AsyncSessionLocal
    from app.services.settings_service import settings_service

    async with AsyncSessionLocal() as db:
        return {
            "smtp_host": await settings_service.get(db, "SMTP_HOST") or "",
            "smtp_port": int(await settings_service.get(db, "SMTP_PORT") or 587),
            "smtp_tls": await settings_service.get(db, "SMTP_TLS"),
            "smtp_user": await settings_service.get(db, "SMTP_USER") or "",
            "smtp_password": await settings_service.get(db, "SMTP_PASSWORD") or "",
            "smtp_from": await settings_service.get(db, "SMTP_FROM") or "",
            "smtp_from_name": await settings_service.get(db, "SMTP_FROM_NAME") or "CloudLand",
            "frontend_url": await settings_service.get(db, "FRONTEND_URL") or "",
        }


async def _send_smtp(message: MIMEMultipart, config: dict) -> bool:
    """Send an email message via SMTP using dynamic settings."""
    try:
        host = config["smtp_host"]
        port = config["smtp_port"]
        tls = config["smtp_tls"]

        smtp_kwargs = {
            "hostname": host,
            "port": port,
        }

        # 端口 465：隐式 SSL；tls=True + 其他端口：STARTTLS；否则明文
        if port == 465:
            smtp_kwargs["use_tls"] = True
        elif tls:
            smtp_kwargs["start_tls"] = True

        if config["smtp_user"]:
            smtp_kwargs["username"] = config["smtp_user"]
            smtp_kwargs["password"] = config["smtp_password"]

        await aiosmtplib.send(message, **smtp_kwargs)
        return True
    except Exception as e:
        logger.error(f"Failed to send email: {e}")
        return False


async def send_activation_email(email: str, username: str, token: str, language: str = "en") -> bool:
    """
    Send activation email to user

    Args:
        email: User's email address
        username: User's username
        token: Activation token
        language: User's preferred language ('en' or 'zh')

    Returns:
        True if email sent successfully, False otherwise
    """
    config = await _get_email_config()

    # Skip email sending if SMTP host is not configured
    if not config["smtp_host"]:
        logger.warning(f"SMTP_HOST not configured, skipping email to {email}")
        logger.info(f"Activation link for frontend would be: {config['frontend_url']}/activate?token={token}")
        return True

    activation_link = f"{config['frontend_url']}/activate?token={token}"
    from_name = config["smtp_from_name"]
    from_addr = config["smtp_from"]

    # Localized Content
    if language == "zh":
        subject = "欢迎来到 Cloudland - 请激活您的账户"
        welcome_title = "🎉 欢迎来到 Cloudland!"
        hello_text = f"您好 <strong>{username}</strong>,"
        main_text = "欢迎加入 Cloudland！我们很高兴您的加入。请点击下方按钮验证您的邮箱并激活账户："
        button_text = "激活账户"
        link_text = "或者将以下链接复制到浏览器中访问："
        token_text = f"验证令牌: <code>{token}</code>"
        note_text = "<strong>注意:</strong> 此链接在 24 小时内有效。"
        footer_ignore = "如果您没有注册过此账户，请忽略此邮件。"
        footer_rights = "© 2026 Cloudland Platform. 保留所有权利。"

        text_fallback = f"""
        欢迎来到 Cloudland!

        您好 {username},

        欢迎加入 Cloudland！请访问以下链接激活您的账户：

        {activation_link}

        验证令牌: {token}

        注意: 此链接在 24 小时内有效。

        如果您没有注册过此账户，请忽略此邮件。

        © 2026 Cloudland Platform. 保留所有权利。
        """
    else:
        subject = "Welcome to Cloudland - Please Activate Your Account"
        welcome_title = "🎉 Welcome to Cloudland!"
        hello_text = f"Hello <strong>{username}</strong>,"
        main_text = "Welcome to Cloudland! We're excited to have you on board. Please click the button below to verify your email and activate your account:"
        button_text = "Activate Account"
        link_text = "Or copy and paste this link into your browser:"
        token_text = f"Verification Token: <code>{token}</code>"
        note_text = "<strong>Note:</strong> This link will expire in 24 hours."
        footer_ignore = "If you did not sign up for this account, please ignore this email."
        footer_rights = "© 2026 Cloudland Platform. All rights reserved."

        text_fallback = f"""
        Welcome to Cloudland!

        Hello {username},

        Welcome to Cloudland! Please visit the link below to activate your account:

        {activation_link}

        Verification Token: {token}

        Note: This link will expire in 24 hours.

        If you did not sign up for this account, please ignore this email.

        © 2026 Cloudland Platform. All rights reserved.
        """

    # Create message
    message = MIMEMultipart("alternative")
    message["Subject"] = subject
    message["From"] = f"{from_name} <{from_addr}>"
    message["To"] = email

    # HTML email content
    html_content = f"""
    <!DOCTYPE html>
    <html>
    <head>
        <meta charset="UTF-8">
        <style>
            body {{
                font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
                line-height: 1.6;
                color: #333;
                max-width: 600px;
                margin: 0 auto;
                padding: 20px;
            }}
            .container {{
                background: #ffffff;
                border-radius: 8px;
                padding: 40px;
                box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            }}
            .header {{
                text-align: center;
                margin-bottom: 30px;
            }}
            .header h1 {{
                color: #2563eb;
                margin: 0;
                font-size: 28px;
            }}
            .content {{
                margin: 30px 0;
            }}
            .button {{
                display: inline-block;
                padding: 14px 32px;
                background: #2563eb;
                color: #ffffff !important;
                text-decoration: none;
                border-radius: 6px;
                font-weight: 600;
                margin: 20px 0;
            }}
            .button:hover {{
                background: #1d4ed8;
            }}
            .footer {{
                margin-top: 40px;
                padding-top: 20px;
                border-top: 1px solid #e5e7eb;
                font-size: 14px;
                color: #6b7280;
                text-align: center;
            }}
            .link {{
                color: #2563eb;
                word-break: break-all;
            }}
        </style>
    </head>
    <body>
        <div class="container">
            <div class="header">
                <h1>{welcome_title}</h1>
            </div>
            <div class="content">
                <p>{hello_text}</p>
                <p>{main_text}</p>
                <div style="text-align: center;">
                    <a href="{activation_link}" class="button">{button_text}</a>
                </div>
                <p>{link_text}</p>
                <p><a href="{activation_link}" class="link">{activation_link}</a></p>
                <p>{token_text}</p>
                <p>{note_text}</p>
            </div>
            <div class="footer">
                <p>{footer_ignore}</p>
                <p>{footer_rights}</p>
            </div>
        </div>
    </body>
    </html>
    """

    # Text content remains using text_fallback
    text_content = text_fallback

    # Attach both versions
    part1 = MIMEText(text_content, "plain", "utf-8")
    part2 = MIMEText(html_content, "html", "utf-8")
    message.attach(part1)
    message.attach(part2)

    ok = await _send_smtp(message, config)
    if ok:
        logger.info(f"Activation email sent successfully to {email}")
    return ok


async def send_invitation_email(
    email: str, org_name: str, inviter_name: str, token: str,
    is_existing_user: bool = False, language: str = "en",
) -> bool:
    """
    Send organization invitation email.

    Args:
        email: Invitee's email address
        org_name: Organization name
        inviter_name: Inviter's display name
        token: Invitation token
        is_existing_user: Whether the invitee already has an account
        language: Language preference ('en' or 'zh')
    """
    config = await _get_email_config()

    if not config["smtp_host"]:
        logger.warning(f"SMTP_HOST not configured, skipping invitation email to {email}")
        accept_link = f"{config['frontend_url']}/invite/accept?token={token}"
        logger.info(f"Invitation link: {accept_link}")
        return True

    accept_link = f"{config['frontend_url']}/invite/accept?token={token}"
    from_name = config["smtp_from_name"]
    from_addr = config["smtp_from"]

    if language == "zh":
        subject = f"CloudLand - 您被邀请加入组织 {org_name}"
        welcome_title = "您被邀请加入一个组织"
        hello_text = f"您好，"
        if is_existing_user:
            main_text = f"<strong>{inviter_name}</strong> 邀请您加入组织 <strong>{org_name}</strong>。点击下方按钮接受邀请："
        else:
            main_text = f"<strong>{inviter_name}</strong> 邀请您加入组织 <strong>{org_name}</strong>。点击下方按钮创建账户并加入："
        button_text = "接受邀请"
        link_text = "或者将以下链接复制到浏览器中访问："
        note_text = "<strong>注意:</strong> 此邀请链接在 24 小时内有效。"
        footer_ignore = "如果您不认识邀请人，请忽略此邮件。"
        footer_rights = "© 2026 Cloudland Platform. 保留所有权利。"
        text_fallback = f"您被邀请加入组织 {org_name}。请访问: {accept_link}"
    else:
        subject = f"CloudLand - You're invited to join {org_name}"
        welcome_title = "You're Invited!"
        hello_text = "Hello,"
        if is_existing_user:
            main_text = f"<strong>{inviter_name}</strong> has invited you to join the organization <strong>{org_name}</strong>. Click the button below to accept:"
        else:
            main_text = f"<strong>{inviter_name}</strong> has invited you to join the organization <strong>{org_name}</strong>. Click the button below to create your account and join:"
        button_text = "Accept Invitation"
        link_text = "Or copy and paste this link into your browser:"
        note_text = "<strong>Note:</strong> This invitation expires in 24 hours."
        footer_ignore = "If you don't recognize the sender, please ignore this email."
        footer_rights = "© 2026 Cloudland Platform. All rights reserved."
        text_fallback = f"You've been invited to join {org_name}. Visit: {accept_link}"

    message = MIMEMultipart("alternative")
    message["Subject"] = subject
    message["From"] = f"{from_name} <{from_addr}>"
    message["To"] = email

    html_content = f"""
    <!DOCTYPE html>
    <html>
    <head>
        <meta charset="UTF-8">
        <style>
            body {{
                font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
                line-height: 1.6;
                color: #333;
                max-width: 600px;
                margin: 0 auto;
                padding: 20px;
            }}
            .container {{
                background: #ffffff;
                border-radius: 8px;
                padding: 40px;
                box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            }}
            .header {{
                text-align: center;
                margin-bottom: 30px;
            }}
            .header h1 {{
                color: #2563eb;
                margin: 0;
                font-size: 28px;
            }}
            .content {{
                margin: 30px 0;
            }}
            .button {{
                display: inline-block;
                padding: 14px 32px;
                background: #2563eb;
                color: #ffffff !important;
                text-decoration: none;
                border-radius: 6px;
                font-weight: 600;
                margin: 20px 0;
            }}
            .footer {{
                margin-top: 40px;
                padding-top: 20px;
                border-top: 1px solid #e5e7eb;
                font-size: 14px;
                color: #6b7280;
                text-align: center;
            }}
            .link {{
                color: #2563eb;
                word-break: break-all;
            }}
        </style>
    </head>
    <body>
        <div class="container">
            <div class="header">
                <h1>{welcome_title}</h1>
            </div>
            <div class="content">
                <p>{hello_text}</p>
                <p>{main_text}</p>
                <div style="text-align: center;">
                    <a href="{accept_link}" class="button">{button_text}</a>
                </div>
                <p>{link_text}</p>
                <p><a href="{accept_link}" class="link">{accept_link}</a></p>
                <p>{note_text}</p>
            </div>
            <div class="footer">
                <p>{footer_ignore}</p>
                <p>{footer_rights}</p>
            </div>
        </div>
    </body>
    </html>
    """

    part1 = MIMEText(text_fallback, "plain", "utf-8")
    part2 = MIMEText(html_content, "html", "utf-8")
    message.attach(part1)
    message.attach(part2)

    ok = await _send_smtp(message, config)
    if ok:
        logger.info(f"Invitation email sent to {email} for org {org_name}")
    return ok

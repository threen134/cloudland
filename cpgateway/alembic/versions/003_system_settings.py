"""system_settings: add dynamic system settings and config version tables

Revision ID: 003
Revises: 002
Create Date: 2026-04-02
"""
import json
import os
from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


revision: str = "003"
down_revision: Union[str, None] = "002"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def _env(key: str, default=None):
    """安全读取环境变量"""
    return os.environ.get(key, default)


def upgrade() -> None:
    # --- 1. 创建 system_settings 表 ---
    op.create_table(
        "system_settings",
        sa.Column("key", sa.String(128), primary_key=True, index=True),
        sa.Column("value", sa.Text(), nullable=True),
        sa.Column("value_type", sa.String(32), nullable=False),
        sa.Column("category", sa.String(32), nullable=False),
        sa.Column("description", sa.String(256), nullable=True),
        sa.Column("is_secret", sa.Boolean(), nullable=False, server_default="false"),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), onupdate=sa.func.now()),
    )

    # --- 2. 创建 system_config_version 表（单行记录）---
    op.create_table(
        "system_config_version",
        sa.Column("id", sa.BigInteger(), primary_key=True),
        sa.Column("version", sa.BigInteger(), nullable=False, server_default="0"),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), onupdate=sa.func.now()),
    )

    # 初始化版本号记录
    op.execute("INSERT INTO system_config_version (id, version) VALUES (1, 0)")

    # --- 3. 从环境变量导入初始数据（安全降级：缺失时使用合理默认值）---
    initial_settings = [
        # 基础信息
        ("PROJECT_NAME",        json.dumps(_env("PROJECT_NAME", "CloudLand")),             "string",  "general",      "项目名称",                  False),
        ("FRONTEND_URL",        json.dumps(_env("FRONTEND_URL", "http://localhost:5173")), "string",  "general",      "前端访问地址",               False),

        # 资源配额
        ("DEFAULT_CPU_CORES",   json.dumps(int(_env("DEFAULT_CPU_CORES", "8"))),           "number",  "quota",        "默认 CPU 配额（核）",        False),
        ("DEFAULT_RAM_GB",      json.dumps(int(_env("DEFAULT_RAM_GB", "16"))),             "number",  "quota",        "默认内存配额（GB）",         False),
        ("DEFAULT_DISK_GB",     json.dumps(int(_env("DEFAULT_DISK_GB", "100"))),           "number",  "quota",        "默认磁盘配额（GB）",         False),
        ("DEFAULT_PUBLIC_IPS",  json.dumps(int(_env("DEFAULT_PUBLIC_IPS", "2"))),          "number",  "quota",        "默认公网 IP 配额",           False),
        ("DEFAULT_TRAFFIC_GB",  json.dumps(int(_env("DEFAULT_TRAFFIC_GB", "1000"))),       "number",  "quota",        "默认流量配额（GB）",         False),

        # 通知渠道
        ("NOTIFICATION_CHANNELS", json.dumps(["email"]),                                   "json",    "notification", "启用的通知渠道列表",         False),
        ("SMTP_HOST",           json.dumps(_env("SMTP_HOST", "")),                         "string",  "notification", "SMTP 主机",                  False),
        ("SMTP_PORT",           json.dumps(int(_env("SMTP_PORT", "587"))),                 "number",  "notification", "SMTP 端口",                  False),
        ("SMTP_TLS",            json.dumps(_env("SMTP_TLS", "true").lower() == "true"),    "boolean", "notification", "启用 TLS",                   False),
        ("SMTP_USER",           json.dumps(_env("SMTP_USER", "")),                         "string",  "notification", "SMTP 用户名",                False),
        ("SMTP_PASSWORD",       json.dumps(_env("SMTP_PASSWORD", "")),                     "secret",  "notification", "SMTP 密码",                  True),
        ("SMTP_FROM",           json.dumps(_env("SMTP_FROM", "")),                         "string",  "notification", "发件人邮箱地址",              False),
        ("SMTP_FROM_NAME",      json.dumps(_env("SMTP_FROM_NAME", "CloudLand")),           "string",  "notification", "发件人名称",                 False),
        ("FEISHU_WEBHOOK_URL",  json.dumps(_env("FEISHU_WEBHOOK_URL", "")),                "string",  "notification", "飞书 Webhook 地址",          False),
        ("FEISHU_SECRET",       json.dumps(_env("FEISHU_SECRET", "")),                     "secret",  "notification", "飞书签名密钥",               True),
        ("SLACK_WEBHOOK_URL",   json.dumps(""),                                            "string",  "notification", "Slack Incoming Webhook URL", False),
        ("CUSTOM_WEBHOOK_URL",  json.dumps(""),                                            "string",  "notification", "自定义 Webhook 地址",        False),
        ("CUSTOM_WEBHOOK_METHOD", json.dumps("POST"),                                      "string",  "notification", "自定义 Webhook HTTP 方法",   False),
        ("CUSTOM_WEBHOOK_HEADERS", json.dumps({}),                                         "json",    "notification", "自定义 Webhook Headers",     True),
    ]

    bind = op.get_bind()
    for key, value, value_type, category, description, is_secret in initial_settings:
        bind.execute(
            sa.text(
                "INSERT INTO system_settings (key, value, value_type, category, description, is_secret) "
                "VALUES (:key, :value, :value_type, :category, :description, :is_secret) "
                "ON CONFLICT (key) DO NOTHING"
            ),
            {
                "key": key,
                "value": value,
                "value_type": value_type,
                "category": category,
                "description": description,
                "is_secret": is_secret,
            },
        )


def downgrade() -> None:
    op.drop_table("system_settings")
    op.drop_table("system_config_version")

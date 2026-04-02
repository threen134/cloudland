"""
设置服务：三级降级（数据库 → 环境变量 → 默认值）。
提供 get_setting / get_all_settings / update_setting 等统一接口。
"""
import json
import logging
from typing import Any, Optional, List, Tuple

from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.system_setting import SystemSetting, SystemConfigVersion
from app.core.config import settings as env_settings

logger = logging.getLogger(__name__)

MASKED = "******"

# 系统设置元数据：(value_type, category, description, is_secret, default_value_fn)
# default_value_fn: 无参 lambda，返回该 key 的环境变量值或硬编码默认值
SETTINGS_METADATA: dict[str, tuple] = {
    # --- 基础信息 ---
    "PROJECT_NAME":         ("string",  "general",      "项目名称",         False, lambda: env_settings.PROJECT_NAME),
    "FRONTEND_URL":         ("string",  "general",      "前端访问地址",     False, lambda: env_settings.FRONTEND_URL),

    # --- 资源配额 ---
    "DEFAULT_CPU_CORES":    ("number",  "quota",        "默认 CPU 配额（核）",   False, lambda: env_settings.DEFAULT_CPU_CORES),
    "DEFAULT_RAM_GB":       ("number",  "quota",        "默认内存配额（GB）",   False, lambda: env_settings.DEFAULT_RAM_GB),
    "DEFAULT_DISK_GB":      ("number",  "quota",        "默认磁盘配额（GB）",   False, lambda: env_settings.DEFAULT_DISK_GB),
    "DEFAULT_PUBLIC_IPS":   ("number",  "quota",        "默认公网 IP 配额",  False, lambda: env_settings.DEFAULT_PUBLIC_IPS),
    "DEFAULT_TRAFFIC_GB":   ("number",  "quota",        "默认流量配额（GB）",   False, lambda: env_settings.DEFAULT_TRAFFIC_GB),

    # --- 通知渠道 ---
    "NOTIFICATION_CHANNELS": ("json",  "notification", "启用的通知渠道列表",  False, lambda: ["email"]),

    # SMTP
    "SMTP_HOST":            ("string",  "notification", "SMTP 主机",         False, lambda: env_settings.SMTP_HOST or ""),
    "SMTP_PORT":            ("number",  "notification", "SMTP 端口",         False, lambda: env_settings.SMTP_PORT or 587),
    "SMTP_TLS":             ("boolean", "notification", "启用 TLS",          False, lambda: env_settings.SMTP_TLS if env_settings.SMTP_TLS is not None else True),
    "SMTP_USER":            ("string",  "notification", "SMTP 用户名",       False, lambda: env_settings.SMTP_USER or ""),
    "SMTP_PASSWORD":        ("secret",  "notification", "SMTP 密码",         True,  lambda: env_settings.SMTP_PASSWORD or ""),
    "SMTP_FROM":            ("string",  "notification", "发件人邮箱地址",   False, lambda: env_settings.SMTP_FROM or ""),
    "SMTP_FROM_NAME":       ("string",  "notification", "发件人名称",       False, lambda: env_settings.SMTP_FROM_NAME or "CloudLand"),

    # 飞书
    "FEISHU_WEBHOOK_URL":   ("string",  "notification", "飞书 Webhook 地址", False, lambda: env_settings.FEISHU_WEBHOOK_URL or ""),
    "FEISHU_SECRET":        ("secret",  "notification", "飞书签名密钥",      True,  lambda: env_settings.FEISHU_SECRET or ""),

    # Slack
    "SLACK_WEBHOOK_URL":    ("string",  "notification", "Slack Incoming Webhook URL", False, lambda: ""),

    # 自定义 Webhook
    "CUSTOM_WEBHOOK_URL":   ("string",  "notification", "自定义 Webhook 地址",         False, lambda: ""),
    "CUSTOM_WEBHOOK_METHOD":("string",  "notification", "自定义 Webhook HTTP 方法",    False, lambda: "POST"),
    "CUSTOM_WEBHOOK_HEADERS":("json",   "notification", "自定义 Webhook Headers（含鉴权）", True, lambda: {}),
}


def _serialize(value: Any) -> str:
    """将 Python 值序列化为 JSON 字符串存入数据库"""
    return json.dumps(value, ensure_ascii=False)


def _deserialize(raw: Optional[str]) -> Any:
    """将数据库中的 JSON 字符串反序列化为 Python 值"""
    if raw is None:
        return None
    try:
        return json.loads(raw)
    except (json.JSONDecodeError, TypeError):
        return raw


class SettingsService:
    """
    系统设置服务，封装三级降级逻辑：
    1. 数据库值（system_settings 表）
    2. 环境变量（Pydantic Settings）
    3. 硬编码默认值
    """

    @staticmethod
    async def get_all(db: AsyncSession) -> List[SystemSetting]:
        """获取所有已存储的设置行（未存储的 key 以元数据默认值补全）"""
        result = await db.execute(select(SystemSetting))
        stored = {row.key: row for row in result.scalars().all()}

        settings_list = []
        for key, (value_type, category, description, is_secret, default_fn) in SETTINGS_METADATA.items():
            if key in stored:
                settings_list.append(stored[key])
            else:
                # 构造虚拟行（不入库），使用 default_fn 获取当前默认值
                try:
                    default_val = default_fn()
                except Exception:
                    default_val = None
                row = SystemSetting(
                    key=key,
                    value=_serialize(default_val),
                    value_type=value_type,
                    category=category,
                    description=description,
                    is_secret=is_secret,
                )
                settings_list.append(row)
        return settings_list

    @staticmethod
    async def get(db: AsyncSession, key: str) -> Any:
        """
        获取单个设置值（三级降级）。
        返回反序列化后的 Python 值，不做脱敏处理。
        """
        result = await db.execute(select(SystemSetting).where(SystemSetting.key == key))
        row = result.scalars().first()
        if row is not None and row.value is not None:
            return _deserialize(row.value)

        # 降级到环境变量/默认值
        meta = SETTINGS_METADATA.get(key)
        if meta:
            try:
                return meta[4]()
            except Exception:
                pass
        return None

    @staticmethod
    async def update(
        db: AsyncSession,
        key: str,
        new_value: Any,
    ) -> Tuple[SystemSetting, int]:
        """
        更新单个设置值，递增 config_version。
        如果新值为 MASKED（"******"），则跳过更新（前端回显脱敏值时不覆盖）。
        返回 (更新后的 row, 新 config_version)。
        """
        # 防止脱敏回写：MASKED 值无论 key 是否存在都不应落库
        if new_value == MASKED:
            result = await db.execute(select(SystemSetting).where(SystemSetting.key == key))
            row = result.scalars().first()
            version = await SettingsService._get_version(db)
            if row:
                return row, version
            # key 尚未入库且值为脱敏占位 — 不创建，不递增版本
            raise ValueError(f"Setting '{key}' does not exist; cannot update with masked value")

        meta = SETTINGS_METADATA.get(key)
        result = await db.execute(select(SystemSetting).where(SystemSetting.key == key))
        row = result.scalars().first()

        serialized = _serialize(new_value)
        if row:
            row.value = serialized
        else:
            value_type, category, description, is_secret, _ = meta if meta else ("string", "general", None, False, None)
            row = SystemSetting(
                key=key,
                value=serialized,
                value_type=value_type,
                category=category,
                description=description,
                is_secret=is_secret,
            )
            db.add(row)

        # 递增全局版本号
        version = await SettingsService._increment_version(db)
        await db.flush()
        return row, version

    @staticmethod
    async def bulk_update(
        db: AsyncSession,
        updates: dict[str, Any],
    ) -> Tuple[List[SystemSetting], int]:
        """
        批量更新多个设置，共用一次版本递增。
        跳过值为 MASKED 的 secret 字段。
        """
        # 过滤掉 secret 字段的脱敏占位、元数据不存在的 key
        effective = {
            k: v for k, v in updates.items()
            if k in SETTINGS_METADATA and not (SETTINGS_METADATA[k][3] and v == MASKED)
        }

        # 批量查询已存在的行，避免 N+1
        existing: dict[str, SystemSetting] = {}
        if effective:
            result = await db.execute(
                select(SystemSetting).where(SystemSetting.key.in_(effective.keys()))
            )
            existing = {row.key: row for row in result.scalars().all()}

        rows = []
        for key, new_value in effective.items():
            meta = SETTINGS_METADATA[key]
            serialized = _serialize(new_value)
            row = existing.get(key)
            if row:
                row.value = serialized
            else:
                value_type, category, description, is_secret2, _ = meta
                row = SystemSetting(
                    key=key,
                    value=serialized,
                    value_type=value_type,
                    category=category,
                    description=description,
                    is_secret=is_secret2,
                )
                db.add(row)
            rows.append(row)

        version = await SettingsService._increment_version(db)
        await db.flush()
        return rows, version

    @staticmethod
    async def _get_version(db: AsyncSession) -> int:
        result = await db.execute(select(SystemConfigVersion).where(SystemConfigVersion.id == 1))
        row = result.scalars().first()
        return row.version if row else 0

    @staticmethod
    async def _increment_version(db: AsyncSession) -> int:
        result = await db.execute(select(SystemConfigVersion).where(SystemConfigVersion.id == 1))
        row = result.scalars().first()
        if row:
            row.version += 1
        else:
            row = SystemConfigVersion(id=1, version=1)
            db.add(row)
        await db.flush()
        return row.version

    @staticmethod
    async def get_sync_payload(db: AsyncSession, force: bool = False) -> dict:
        """
        构造推送给 clapi 的全量同步 payload。
        使用 get_all() 而非直接查表，确保仅存在于环境变量/默认值（未写入 DB）的
        配置项也能包含在 payload 中，避免 clapi 镜像缺项。
        secret 字段以明文同步（clapi 通知发送需要真实值），不做脱敏。
        force=True 时强制全量覆盖（用于 Provisioning / 心跳恢复场景）。
        """
        version = await SettingsService._get_version(db)
        all_rows = await SettingsService.get_all(db)

        settings_payload = []
        for r in all_rows:
            # 获取真实值（不脱敏）：直接读取 row.value（JSON 序列化格式）
            raw_value = r.value
            # 对于虚拟行（未入库，由 get_all 补全），直接使用其 value
            settings_payload.append({
                "key": r.key,
                "value": raw_value,
                "value_type": r.value_type,
                "category": r.category,
                "is_secret": r.is_secret,
            })

        return {
            "config_version": version,
            "settings": settings_payload,
            "force": force,
        }


settings_service = SettingsService()

import asyncio
import httpx
from typing import Optional, Dict, Any
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.region import Region
from app.models.notification import NotificationChannel
from app.core.config import settings
from app.core.logging_config import logger


class NotificationSyncService:
    """
    通知渠道同步服务。
    负责将 CPGateway 的 NotificationChannel 变更推送到各 Region 的 clapi。
    严格遵守单向依赖：只有 CPGateway 主动 Push，clapi 绝不反向调用。
    """

    @staticmethod
    async def push_channel_to_all_regions(
        db: AsyncSession,
        action: str,
        channel: Optional[NotificationChannel] = None,
        channel_uuid: Optional[str] = None,
    ):
        """
        异步扇出：将单个渠道变更推送到所有活跃 Region。
        action: "upsert" 或 "delete"
        """
        result = await db.execute(
            select(Region).where(
                Region.is_available.is_(True),
                Region.maintenance_mode.is_(False),
            )
        )
        regions = result.scalars().all()
        if not regions:
            return

        if action == "upsert" and channel:
            payload = {
                "action": "upsert",
                "channel": {
                    "uuid": channel.uuid,
                    "org_id": channel.org_id,
                    "name": channel.name,
                    "type": channel.type,
                    "config": channel.config,
                    "enabled": channel.enabled,
                },
            }
        elif action == "delete" and channel_uuid:
            payload = {
                "action": "delete",
                "channel_uuid": channel_uuid,
            }
        else:
            return

        # 复用同一个 httpx client，避免每个 Region 创建独立连接池
        async with httpx.AsyncClient(verify=False, timeout=10.0) as client:
            tasks = [
                NotificationSyncService._push_to_region(region, payload, client)
                for region in regions
            ]
            results = await asyncio.gather(*tasks, return_exceptions=True)
            for region, res in zip(regions, results):
                if isinstance(res, Exception):
                    logger.error(f"Failed to sync channel to region '{region.name}': {res}")

    @staticmethod
    async def push_all_channels_to_region(db: AsyncSession, region: Region):
        """
        全量推送：将所有有效渠道推送给指定 Region（用于 cold_start 补偿）。
        """
        # 全量推送所有渠道（含 disabled），确保 clapi 镜像完整
        # BulkSyncChannels 会清理不在列表中的渠道，若只推 enabled 则 disabled 渠道会被误删
        result = await db.execute(select(NotificationChannel))
        channels = result.scalars().all()

        payload = {
            "action": "bulk_sync",
            "channels": [
                {
                    "uuid": ch.uuid,
                    "org_id": ch.org_id,
                    "name": ch.name,
                    "type": ch.type,
                    "config": ch.config,
                    "enabled": ch.enabled,
                }
                for ch in channels
            ],
        }

        try:
            await NotificationSyncService._push_to_region(region, payload)
            logger.info(
                f"Full channel sync to region '{region.name}' completed: {len(channels)} channels"
            )
        except Exception as e:
            logger.error(f"Full channel sync to region '{region.name}' failed: {e}")

    @staticmethod
    async def _push_to_region(
        region: Region,
        payload: Dict[str, Any],
        client: Optional[httpx.AsyncClient] = None,
        max_retries: int = 3,
    ):
        """推送到单个 Region 的 clapi 内部接口（带重试）"""
        base_url = region.internal_endpoint.rstrip("/")
        url = f"{base_url}/api/v1/internal/notification-channels/sync"

        async def _do_post(c: httpx.AsyncClient):
            response = await c.post(
                url,
                json=payload,
                headers={
                    "X-Forwarded-Secret": region.internal_secret,
                },
            )
            if response.status_code not in (200, 201):
                raise Exception(
                    f"HTTP {response.status_code}: {response.text[:200]}"
                )

        last_exc = None
        for attempt in range(max_retries):
            try:
                if client:
                    await _do_post(client)
                else:
                    async with httpx.AsyncClient(verify=False, timeout=10.0) as c:
                        await _do_post(c)
                return
            except Exception as e:
                last_exc = e
                if attempt < max_retries - 1:
                    delay = (attempt + 1) * 2  # 2s, 4s backoff
                    logger.warning(
                        f"Retry {attempt + 1}/{max_retries} pushing to region '{region.name}': {e}"
                    )
                    await asyncio.sleep(delay)
        raise last_exc

    @staticmethod
    async def get_alarm_firing_count(region: Region) -> int:
        """查询单个 Region 的 firing 告警数"""
        base_url = region.internal_endpoint.rstrip("/")
        url = f"{base_url}/api/v1/internal/alarm/events"

        try:
            async with httpx.AsyncClient(verify=False, timeout=2.0) as client:
                response = await client.get(
                    url,
                    params={"status": "firing", "count_only": "true"},
                    headers={
                        "X-Forwarded-Secret": region.internal_secret,
                        "X-User-ID": "0",
                        "X-System-Role": "1",
                    },
                )
                if response.status_code == 200:
                    data = response.json()
                    return data.get("count", 0)
                return -1
        except Exception:
            return -1


notification_sync_service = NotificationSyncService()

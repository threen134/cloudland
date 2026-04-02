"""
系统设置多区域同步服务。
复用 NotificationSyncService 的推送模式（异步扇出 + 指数退避重试）。
"""
import asyncio
import httpx
from typing import Optional

from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.region import Region
from app.services.settings_service import settings_service
from app.core.logging_config import logger


class SettingsSyncService:

    @staticmethod
    async def push_settings_to_all_regions(db: AsyncSession):
        """异步扇出：将全量系统设置推送到所有活跃 Region"""
        result = await db.execute(
            select(Region).where(
                Region.is_available.is_(True),
                Region.maintenance_mode.is_(False),
            )
        )
        regions = result.scalars().all()
        if not regions:
            return

        payload = await settings_service.get_sync_payload(db)

        async with httpx.AsyncClient(verify=False, timeout=10.0) as client:
            tasks = [
                SettingsSyncService._push_to_region(region, payload, client)
                for region in regions
            ]
            results = await asyncio.gather(*tasks, return_exceptions=True)
            for region, res in zip(regions, results):
                if isinstance(res, Exception):
                    logger.error(f"Failed to sync system settings to region '{region.name}': {res}")

    @staticmethod
    async def push_settings_to_region(db: AsyncSession, region: Region):
        """全量推送系统设置给指定 Region（用于新区域注册或心跳恢复，force=True 跳过版本检查）"""
        payload = await settings_service.get_sync_payload(db, force=True)
        try:
            async with httpx.AsyncClient(verify=False, timeout=10.0) as client:
                await SettingsSyncService._push_to_region(region, payload, client)
            logger.info(f"Full system settings sync to region '{region.name}' completed (version={payload['config_version']})")
        except Exception as e:
            logger.error(f"Full system settings sync to region '{region.name}' failed: {e}")

    @staticmethod
    async def _push_to_region(
        region: Region,
        payload: dict,
        client: Optional[httpx.AsyncClient] = None,
        max_retries: int = 3,
    ):
        """推送到单个 Region 的 clapi 内部接口（带重试）"""
        base_url = region.internal_endpoint.rstrip("/")
        url = f"{base_url}/api/v1/internal/system-settings/sync"

        async def _do_post(c: httpx.AsyncClient):
            response = await c.post(
                url,
                json=payload,
                headers={"X-Forwarded-Secret": region.internal_secret},
            )
            if response.status_code not in (200, 201):
                raise Exception(f"HTTP {response.status_code}: {response.text[:200]}")

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
                        f"Retry {attempt + 1}/{max_retries} pushing settings to region '{region.name}': {e}"
                    )
                    await asyncio.sleep(delay)
        raise last_exc


settings_sync_service = SettingsSyncService()

"""
Org 多区域同步服务。
CPGateway 创建/更新 Org 时，将 org 基本信息推送到各 Region 的 clapi，
保持 clapi.organizations 表与 CPGateway 一致，确保新 org 用户能正常创建资源。
"""
import asyncio
import httpx
from typing import Optional

from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.org import Organization
from app.models.region import Region
from app.core.database import AsyncSessionLocal
from app.core.logging_config import logger


class OrgSyncService:

    @staticmethod
    async def sync_org_to_all_regions(org_id: int, org_name: str, org_slug: str):
        """将单个 org 同步到所有活跃 Region（background task 调用，内部新开 session）"""
        async with AsyncSessionLocal() as db:
            result = await db.execute(
                select(Region).where(
                    Region.is_available.is_(True),
                    Region.maintenance_mode.is_(False),
                )
            )
            regions = result.scalars().all()
        if not regions:
            return

        payload = {"id": org_id, "name": org_name, "slug": org_slug or ""}

        async with httpx.AsyncClient(verify=False, timeout=10.0) as client:
            tasks = [
                OrgSyncService._push_to_region(region, payload, client)
                for region in regions
            ]
            results = await asyncio.gather(*tasks, return_exceptions=True)
            for region, res in zip(regions, results):
                if isinstance(res, Exception):
                    logger.error(
                        f"Failed to sync org '{org_name}' (id={org_id}) "
                        f"to region '{region.name}': {res}"
                    )

    @staticmethod
    async def sync_all_orgs_to_region(db: AsyncSession, region: Region):
        """将所有活跃 org 推送到指定 Region（新 region 注册时调用）"""
        result = await db.execute(
            select(Organization).where(Organization.deleted_at.is_(None))
        )
        orgs = result.scalars().all()
        if not orgs:
            return

        async with httpx.AsyncClient(verify=False, timeout=10.0) as client:
            tasks = [
                OrgSyncService._push_to_region(
                    region, {"id": org.id, "name": org.name, "slug": org.slug or ""}, client
                )
                for org in orgs
            ]
            results = await asyncio.gather(*tasks, return_exceptions=True)
            for org, res in zip(orgs, results):
                if isinstance(res, Exception):
                    logger.error(
                        f"Failed to sync org '{org.name}' (id={org.id}) "
                        f"to new region '{region.name}': {res}"
                    )
        logger.info(
            f"Org sync to new region '{region.name}' completed: {len(orgs)} orgs"
        )

    @staticmethod
    async def _push_to_region(
        region: Region,
        payload: dict,
        client: Optional[httpx.AsyncClient] = None,
        max_retries: int = 3,
    ):
        """推送到单个 Region 的 clapi 内部接口（带重试）"""
        base_url = region.internal_endpoint.rstrip("/")
        url = f"{base_url}/api/v1/internal/orgs/sync"

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
                    delay = (attempt + 1) * 2
                    logger.warning(
                        f"Retry {attempt + 1}/{max_retries} syncing org to "
                        f"region '{region.name}': {e}"
                    )
                    await asyncio.sleep(delay)
        raise last_exc


org_sync_service = OrgSyncService()

import httpx
from typing import Dict

from fastapi import HTTPException
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.org_resource_quota import OrgResourceQuota
from app.models.org_resource_consumption import OrgResourceConsumption
from app.models.region import Region

from app.core.config import settings
from app.core.logging_config import logger


def build_backend_url(internal_endpoint: str, api_path: str = "") -> str:
    """构建 Cloudland 后端 URL（消除重复的 URL 拼接逻辑）"""
    base_url = internal_endpoint.strip().rstrip("/")
    if not base_url.endswith(settings.BACKEND_API_SUFFIX):
        base_url = f"{base_url}{settings.BACKEND_API_SUFFIX}"
    return f"{base_url}/{api_path}" if api_path else base_url


def extract_flavor_specs(data: dict) -> Dict[str, float]:
    """从 flavor 数据中提取 cpu/ram/disk 规格（消除重复的字段提取逻辑）"""
    return {
        "cpu_cores": float(data.get("cpu", data.get("vcpus", 0))),
        "ram_gb": float(data.get("memory", data.get("ram", 0))) / 1024.0,
        "disk_gb": float(data.get("disk", 0)),
    }


class QuotaExceededError(HTTPException):
    """配额超限异常 — 返回 HTTP 429"""
    def __init__(self, resource: str, requested: float, available: float, limit: float, region: str):
        detail = {
            "error": "quota_exceeded",
            "resource": resource,
            "region": region,
            "requested": requested,
            "available": available,
            "limit": limit,
            "message": (
                f"Org quota exceeded for {resource} in region {region}: "
                f"requested {requested}, available {available} (limit: {limit})"
            ),
        }
        super().__init__(status_code=429, detail=detail)


# 资源字段映射: consumption 字段 → quota 字段
RESOURCE_FIELD_MAP = {
    "cpu_cores": "max_cpu_cores",
    "ram_gb": "max_ram_gb",
    "public_ips": "max_public_ips",
    "disk_gb": "max_disk_gb",
}


class QuotaService:
    """Org-Region 级别配额检查与消费追踪"""

    async def check_and_reserve(
        self, db: AsyncSession, org_id: int, region_id: int, amount: Dict[str, float],
    ) -> None:
        """
        原子操作：检查配额 + 预扣资源。
        使用 SELECT FOR UPDATE 锁定 consumption 行，防止并发超卖。
        配额不足抛出 QuotaExceededError。
        """
        if not amount:
            return

        # 1. Lock consumption row
        consumption_result = await db.execute(
            select(OrgResourceConsumption)
            .where(
                OrgResourceConsumption.org_id == org_id,
                OrgResourceConsumption.region_id == region_id,
            )
            .with_for_update()
        )
        consumption = consumption_result.scalars().first()
        if not consumption:
            raise HTTPException(status_code=404, detail="Consumption record not found")

        # 2. Load quota
        quota_result = await db.execute(
            select(OrgResourceQuota).where(
                OrgResourceQuota.org_id == org_id,
                OrgResourceQuota.region_id == region_id,
            )
        )
        quota = quota_result.scalars().first()
        if not quota:
            raise HTTPException(status_code=404, detail="Quota record not found")

        # 3. Get region name for error messages
        region_result = await db.execute(select(Region.name).where(Region.id == region_id))
        region_name = region_result.scalar() or str(region_id)

        # 4. Check each resource and reserve in single pass
        for res_field, req_amount in amount.items():
            if req_amount <= 0:
                continue
            quota_field = RESOURCE_FIELD_MAP.get(res_field)
            if not quota_field:
                continue

            current = getattr(consumption, res_field, 0)
            limit = getattr(quota, quota_field, 0)
            available = limit - current

            if current + req_amount > limit:
                raise QuotaExceededError(
                    resource=res_field,
                    requested=req_amount,
                    available=max(0, available),
                    limit=limit,
                    region=region_name,
                )

            # Reserve immediately after check passes
            setattr(consumption, res_field, current + req_amount)

        await db.commit()
        logger.info(f"Quota reserved: org={org_id}, region={region_id}, amount={amount}")

    async def add_consumption(
        self, db: AsyncSession, org_id: int, region_id: int, amount: Dict[str, float],
    ) -> None:
        """直接增加资源消费（不检查配额），用于超级管理员操作追踪"""
        if not amount:
            return

        result = await db.execute(
            select(OrgResourceConsumption)
            .where(
                OrgResourceConsumption.org_id == org_id,
                OrgResourceConsumption.region_id == region_id,
            )
            .with_for_update()
        )
        consumption = result.scalars().first()
        if not consumption:
            logger.warning(f"Add consumption skipped: no record for org={org_id}, region={region_id}")
            return

        for res_field, add_amount in amount.items():
            if add_amount <= 0:
                continue
            if res_field in RESOURCE_FIELD_MAP:
                current = getattr(consumption, res_field, 0)
                setattr(consumption, res_field, current + add_amount)

        await db.commit()
        logger.info(f"Consumption added (no quota check): org={org_id}, region={region_id}, amount={amount}")

    async def release(
        self, db: AsyncSession, org_id: int, region_id: int, amount: Dict[str, float],
    ) -> None:
        """减少 org 在指定 region 的资源消费"""
        if not amount:
            return

        result = await db.execute(
            select(OrgResourceConsumption)
            .where(
                OrgResourceConsumption.org_id == org_id,
                OrgResourceConsumption.region_id == region_id,
            )
            .with_for_update()
        )
        consumption = result.scalars().first()
        if not consumption:
            logger.warning(f"Release failed: no consumption record for org={org_id}, region={region_id}")
            return

        for res_field, rel_amount in amount.items():
            if rel_amount <= 0:
                continue
            if res_field in RESOURCE_FIELD_MAP:
                current = getattr(consumption, res_field, 0)
                setattr(consumption, res_field, max(0, current - rel_amount))

        await db.commit()
        logger.info(f"Quota released: org={org_id}, region={region_id}, amount={amount}")

    async def query_resource_amount(
        self, region: Region, proxy_path: str, resource_id: str, headers: dict,
    ) -> Dict[str, float]:
        """
        查询资源的规格（DELETE 前 / RESIZE 前获取当前资源量）。
        向 Cloudland 后端发送 GET 请求获取资源详情。
        查询失败时抛出 HTTPException，防止静默跳过导致配额泄漏。
        """
        # Determine resource type from path
        if "/instances" in proxy_path:
            api_path = f"instances/{resource_id}"
        elif "/volumes" in proxy_path:
            api_path = f"volumes/{resource_id}"
        elif "/floating_ips" in proxy_path:
            # Floating IP is always 1 public_ip, no need to query
            return {"public_ips": 1}
        else:
            return {}

        url = build_backend_url(region.internal_endpoint, api_path)

        try:
            async with httpx.AsyncClient(verify=False) as client:
                resp = await client.get(
                    url, headers=headers, timeout=settings.BACKEND_REQUEST_TIMEOUT,
                )
                if resp.status_code != 200:
                    logger.error(f"Failed to query resource amount: {url} -> {resp.status_code}")
                    raise HTTPException(
                        status_code=502,
                        detail=f"Failed to query resource {resource_id} for quota tracking (status={resp.status_code})",
                    )
                data = resp.json()
        except httpx.RequestError as e:
            logger.error(f"Failed to query resource amount: {url}: {e}")
            raise HTTPException(
                status_code=502,
                detail=f"Failed to query resource {resource_id} for quota tracking: {e}",
            )

        if "/instances" in proxy_path:
            flavor = data.get("flavor")
            if isinstance(flavor, dict):
                return extract_flavor_specs(flavor)
            # flavor is null or a scalar ID — skip quota tracking for this delete
            logger.warning(f"Cannot determine resource amount for instance {resource_id}: flavor={flavor!r}")
            return {}
        elif "/volumes" in proxy_path:
            return {"disk_gb": float(data.get("size", 0))}

        return {}

    async def query_flavor_amount(
        self, region: Region, flavor_id, headers: dict,
    ) -> Dict[str, float]:
        """
        查询 flavor 规格（CREATE / RESIZE 时获取目标 flavor 的资源量）。
        向 Cloudland 后端发送 GET /flavors/{flavor_id}。
        """
        url = build_backend_url(region.internal_endpoint, f"flavors/{flavor_id}")

        try:
            async with httpx.AsyncClient(verify=False) as client:
                resp = await client.get(
                    url, headers=headers, timeout=settings.BACKEND_REQUEST_TIMEOUT,
                )
                if resp.status_code != 200:
                    logger.warning(f"Failed to query flavor: {url} -> {resp.status_code}")
                    raise HTTPException(status_code=502, detail=f"Failed to query flavor {flavor_id}")
                data = resp.json()
        except httpx.RequestError as e:
            logger.error(f"Failed to query flavor: {url}: {e}")
            raise HTTPException(status_code=502, detail=f"Failed to query flavor {flavor_id}: {e}")

        return extract_flavor_specs(data)


quota_service = QuotaService()

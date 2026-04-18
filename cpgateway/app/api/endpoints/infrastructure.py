"""
Infrastructure 配置只读接口（仅 SuperAdmin）。
透传到指定 Region 的 clapi 内部接口，展示当前 clapi 进程运行时加载的
S3/MinIO/CLAPI/SCI 配置（secret 脱敏）。这些值源自 .env，修改需重启。

GET  /api/v1/system/infrastructure?region=<uuid>
POST /api/v1/system/infrastructure/test-s3?region=<uuid>
"""
import httpx
from fastapi import APIRouter, Depends, HTTPException, Query
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.core.database import get_db
from app.api.deps import get_current_superuser
from app.models.user import User
from app.models.region import Region
from app.core.logging_config import logger

router = APIRouter()


async def _resolve_region(db: AsyncSession, region_uuid: str) -> Region:
    result = await db.execute(select(Region).where(Region.uuid == region_uuid))
    region = result.scalars().first()
    if not region:
        raise HTTPException(status_code=404, detail=f"Region '{region_uuid}' not found")
    return region


async def _clapi_request(region: Region, method: str, path: str) -> dict:
    """透传请求到 region 的 clapi，带 internal_secret 鉴权"""
    base_url = region.internal_endpoint.rstrip("/")
    url = f"{base_url}/api/v1{path}"
    headers = {"X-Forwarded-Secret": region.internal_secret}

    async with httpx.AsyncClient(verify=False, timeout=15.0) as client:
        try:
            resp = await client.request(method, url, headers=headers)
        except httpx.HTTPError as e:
            logger.error(f"infrastructure proxy to '{region.name}' failed: {e}")
            raise HTTPException(status_code=502, detail=f"Region clapi unreachable: {e}")

    if resp.status_code >= 400:
        logger.error(f"clapi '{region.name}' returned {resp.status_code} for {method} {path}: {resp.text[:2000]}")
        raise HTTPException(
            status_code=502,
            detail=f"Region clapi returned {resp.status_code}: {resp.text[:200]}",
        )
    try:
        return resp.json()
    except ValueError:
        raise HTTPException(status_code=502, detail="Region clapi returned non-JSON body")


@router.get("")
async def get_infrastructure(
    region: str = Query(..., description="Region UUID"),
    db: AsyncSession = Depends(get_db),
    _: User = Depends(get_current_superuser),
):
    """拉取指定 Region 的 clapi 运行时基础设施配置（secret 已脱敏）"""
    region_obj = await _resolve_region(db, region)
    data = await _clapi_request(region_obj, "GET", "/internal/runtime-config")
    data["region_name"] = region_obj.display_name or region_obj.name
    data["region_uuid"] = region_obj.uuid
    return data


@router.post("/test-s3")
async def test_s3(
    region: str = Query(..., description="Region UUID"),
    db: AsyncSession = Depends(get_db),
    _: User = Depends(get_current_superuser),
):
    """触发指定 Region 的 clapi 做一次 S3 BucketExists 连通性探测"""
    region_obj = await _resolve_region(db, region)
    return await _clapi_request(region_obj, "POST", "/internal/runtime-config/test-s3")

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from typing import List

from app.core.database import get_db
from app.api.deps import get_current_active_user, get_current_superuser
from app.models.user import User, SystemRole
from app.models.org import Organization
from app.models.member import Member
from app.models.region import Region
from app.models.org_resource_quota import OrgResourceQuota
from app.models.org_resource_consumption import OrgResourceConsumption
from app.schemas.resource import (
    OrgResourceQuota as OrgResourceQuotaSchema,
    OrgResourceConsumption as OrgResourceConsumptionSchema,
    OrgResourceQuotaUpdate,
    OrgResourceInfo,
    OrgResourceSummary,
    QuotaFields,
    ConsumptionFields,
)

router = APIRouter()


async def _check_org_access(db: AsyncSession, user: User, org: Organization) -> None:
    """检查用户是否有权访问该 org 的配额信息（成员 或 superuser）"""
    if user.is_superuser or user.system_role == SystemRole.ADMIN:
        return
    result = await db.execute(
        select(Member).where(
            Member.user_id == user.id,
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    if not result.scalars().first():
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="You are not a member of this organization",
        )


async def _get_org_or_404(db: AsyncSession, org_uuid: str) -> Organization:
    result = await db.execute(
        select(Organization).where(
            Organization.uuid == org_uuid,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Organization not found")
    return org


async def _get_region_by_name_or_404(db: AsyncSession, region_name: str) -> Region:
    result = await db.execute(select(Region).where(Region.name == region_name))
    region = result.scalars().first()
    if not region:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail=f"Region '{region_name}' not found")
    return region


def _build_quota_schema(q: OrgResourceQuota, org_uuid: str, region_name: str) -> OrgResourceQuotaSchema:
    return OrgResourceQuotaSchema(
        org_uuid=org_uuid,
        region_name=region_name,
        max_cpu_cores=q.max_cpu_cores,
        max_ram_gb=q.max_ram_gb,
        max_public_ips=q.max_public_ips,
        max_disk_gb=q.max_disk_gb,
        created_at=q.created_at,
        updated_at=q.updated_at,
    )


def _build_consumption_schema(c: OrgResourceConsumption, org_uuid: str, region_name: str) -> OrgResourceConsumptionSchema:
    return OrgResourceConsumptionSchema(
        org_uuid=org_uuid,
        region_name=region_name,
        cpu_cores=c.cpu_cores,
        ram_gb=c.ram_gb,
        public_ips=c.public_ips,
        disk_gb=c.disk_gb,
    )


def _build_resource_info(q: OrgResourceQuota, c: OrgResourceConsumption, region_name: str) -> OrgResourceInfo:
    return OrgResourceInfo(
        region_name=region_name,
        quota=QuotaFields(
            max_cpu_cores=q.max_cpu_cores,
            max_ram_gb=q.max_ram_gb,
            max_public_ips=q.max_public_ips,
            max_disk_gb=q.max_disk_gb,
        ),
        consumption=ConsumptionFields(
            cpu_cores=c.cpu_cores,
            ram_gb=c.ram_gb,
            public_ips=c.public_ips,
            disk_gb=c.disk_gb,
        ),
    )


# === Quota Endpoints ===

@router.get("/quota/{org_uuid}", response_model=List[OrgResourceQuotaSchema])
async def get_org_quotas(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 所有 region 的配额"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)
    result = await db.execute(
        select(OrgResourceQuota, Region)
        .join(Region, OrgResourceQuota.region_id == Region.id)
        .where(OrgResourceQuota.org_id == org.id)
    )
    rows = result.all()
    return [_build_quota_schema(q, org_uuid, r.name) for q, r in rows]


@router.get("/quota/{org_uuid}/{region_name}", response_model=OrgResourceQuotaSchema)
async def get_org_region_quota(
    org_uuid: str,
    region_name: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 在特定 region 的配额"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)
    region = await _get_region_by_name_or_404(db, region_name)
    result = await db.execute(
        select(OrgResourceQuota).where(
            OrgResourceQuota.org_id == org.id,
            OrgResourceQuota.region_id == region.id,
        )
    )
    quota = result.scalars().first()
    if not quota:
        raise HTTPException(status_code=404, detail="Quota record not found for this org-region")
    return _build_quota_schema(quota, org_uuid, region_name)


@router.put("/quota/{org_uuid}/{region_name}", response_model=OrgResourceQuotaSchema)
async def update_org_region_quota(
    org_uuid: str,
    region_name: str,
    quota_update: OrgResourceQuotaUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """设置 org 在特定 region 的配额（superuser only）"""
    org = await _get_org_or_404(db, org_uuid)
    region = await _get_region_by_name_or_404(db, region_name)
    result = await db.execute(
        select(OrgResourceQuota).where(
            OrgResourceQuota.org_id == org.id,
            OrgResourceQuota.region_id == region.id,
        )
    )
    quota = result.scalars().first()
    if not quota:
        raise HTTPException(status_code=404, detail="Quota record not found for this org-region")

    update_data = quota_update.model_dump(exclude_unset=True)
    for field, value in update_data.items():
        setattr(quota, field, value)

    await db.commit()
    await db.refresh(quota)
    return _build_quota_schema(quota, org_uuid, region_name)


# === Consumption Endpoints ===

@router.get("/consumption/{org_uuid}", response_model=List[OrgResourceConsumptionSchema])
async def get_org_consumptions(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 所有 region 的消费"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)
    result = await db.execute(
        select(OrgResourceConsumption, Region)
        .join(Region, OrgResourceConsumption.region_id == Region.id)
        .where(OrgResourceConsumption.org_id == org.id)
    )
    rows = result.all()
    return [_build_consumption_schema(c, org_uuid, r.name) for c, r in rows]


@router.get("/consumption/{org_uuid}/{region_name}", response_model=OrgResourceConsumptionSchema)
async def get_org_region_consumption(
    org_uuid: str,
    region_name: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 在特定 region 的消费"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)
    region = await _get_region_by_name_or_404(db, region_name)
    result = await db.execute(
        select(OrgResourceConsumption).where(
            OrgResourceConsumption.org_id == org.id,
            OrgResourceConsumption.region_id == region.id,
        )
    )
    consumption = result.scalars().first()
    if not consumption:
        raise HTTPException(status_code=404, detail="Consumption record not found for this org-region")
    return _build_consumption_schema(consumption, org_uuid, region_name)


# === Combined Info Endpoints ===

@router.get("/info/{org_uuid}", response_model=OrgResourceSummary)
async def get_org_resource_summary(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 所有 region 的配额+消费汇总（单次 JOIN 查询）"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)

    result = await db.execute(
        select(OrgResourceQuota, OrgResourceConsumption, Region)
        .join(
            OrgResourceConsumption,
            (OrgResourceQuota.org_id == OrgResourceConsumption.org_id)
            & (OrgResourceQuota.region_id == OrgResourceConsumption.region_id),
        )
        .join(Region, OrgResourceQuota.region_id == Region.id)
        .where(OrgResourceQuota.org_id == org.id)
    )

    regions = [
        _build_resource_info(q, c, r.name)
        for q, c, r in result.all()
    ]

    return OrgResourceSummary(org_uuid=org_uuid, regions=regions)


@router.get("/info/{org_uuid}/{region_name}", response_model=OrgResourceInfo)
async def get_org_region_resource_info(
    org_uuid: str,
    region_name: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取 org 在特定 region 的配额+消费（单次 JOIN 查询）"""
    org = await _get_org_or_404(db, org_uuid)
    await _check_org_access(db, current_user, org)
    region = await _get_region_by_name_or_404(db, region_name)

    result = await db.execute(
        select(OrgResourceQuota, OrgResourceConsumption)
        .join(
            OrgResourceConsumption,
            (OrgResourceQuota.org_id == OrgResourceConsumption.org_id)
            & (OrgResourceQuota.region_id == OrgResourceConsumption.region_id),
        )
        .where(
            OrgResourceQuota.org_id == org.id,
            OrgResourceQuota.region_id == region.id,
        )
    )
    row = result.first()
    if not row:
        raise HTTPException(status_code=404, detail="Quota/consumption record not found for this org-region")

    quota, consumption = row
    return _build_resource_info(quota, consumption, region_name)

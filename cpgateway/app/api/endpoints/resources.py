from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from app.core.database import get_db
from app.api.deps import get_current_superuser
from app.models.user import User
from app.models.resource_consumption import ResourceConsumption
from app.models.resource_quota import ResourceQuota
from app.schemas.resource import (
    ResourceConsumption as ResourceConsumptionSchema,
    ResourceQuota as ResourceQuotaSchema,
    ResourceQuotaUpdate,
    UserResourceInfo
)
from app.core.config import settings

router = APIRouter()


async def _get_user_or_404(db, user_uuid: str) -> User:
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"User with UUID {user_uuid} not found"
        )
    return user


def _consumption_to_schema(c, user_uuid: str) -> ResourceConsumptionSchema:
    return ResourceConsumptionSchema(
        user_uuid=user_uuid,
        cpu_cores=c.cpu_cores, ram_gb=c.ram_gb, traffic_gb=c.traffic_gb,
        public_ips=c.public_ips, disk_gb=c.disk_gb,
        created_at=c.created_at, updated_at=c.updated_at,
    )


def _quota_to_schema(q, user_uuid: str) -> ResourceQuotaSchema:
    return ResourceQuotaSchema(
        user_uuid=user_uuid,
        max_cpu_cores=q.max_cpu_cores, max_ram_gb=q.max_ram_gb,
        max_traffic_gb=q.max_traffic_gb, max_public_ips=q.max_public_ips,
        max_disk_gb=q.max_disk_gb,
        created_at=q.created_at, updated_at=q.updated_at,
    )


@router.get("/consumption/{user_uuid}", response_model=ResourceConsumptionSchema)
async def get_user_consumption(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    user = await _get_user_or_404(db, user_uuid)

    result = await db.execute(
        select(ResourceConsumption).where(ResourceConsumption.user_id == user.id)
    )
    consumption = result.scalars().first()

    if not consumption:
        consumption = ResourceConsumption(
            user_id=user.id,
            cpu_cores=0.0, ram_gb=0.0, traffic_gb=0.0, public_ips=0, disk_gb=0.0
        )
        db.add(consumption)
        await db.commit()
        await db.refresh(consumption)

    return _consumption_to_schema(consumption, user_uuid)


@router.get("/quota/{user_uuid}", response_model=ResourceQuotaSchema)
async def get_user_quota(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    user = await _get_user_or_404(db, user_uuid)

    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user.id)
    )
    quota = result.scalars().first()

    if not quota:
        quota = ResourceQuota(
            user_id=user.id,
            max_cpu_cores=0, max_ram_gb=0, max_traffic_gb=0,
            max_public_ips=0, max_disk_gb=0,
        )
        db.add(quota)
        await db.commit()
        await db.refresh(quota)

    return _quota_to_schema(quota, user_uuid)


@router.put("/quota/{user_uuid}", response_model=ResourceQuotaSchema)
async def update_user_quota(
    user_uuid: str,
    quota_update: ResourceQuotaUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    user = await _get_user_or_404(db, user_uuid)

    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user.id)
    )
    quota = result.scalars().first()

    if not quota:
        quota = ResourceQuota(
            user_id=user.id,
            max_cpu_cores=0, max_ram_gb=0, max_traffic_gb=0,
            max_public_ips=0, max_disk_gb=0,
        )
        db.add(quota)

    update_data = quota_update.model_dump(exclude_unset=True)
    for field, value in update_data.items():
        setattr(quota, field, value)

    await db.commit()
    await db.refresh(quota)

    return _quota_to_schema(quota, user_uuid)


@router.get("/info/{user_uuid}", response_model=UserResourceInfo)
async def get_user_resource_info(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    user = await _get_user_or_404(db, user_uuid)

    result = await db.execute(
        select(ResourceConsumption).where(ResourceConsumption.user_id == user.id)
    )
    consumption = result.scalars().first()

    if not consumption:
        consumption = ResourceConsumption(
            user_id=user.id,
            cpu_cores=0.0, ram_gb=0.0, traffic_gb=0.0, public_ips=0, disk_gb=0.0
        )
        db.add(consumption)

    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user.id)
    )
    quota = result.scalars().first()

    if not quota:
        quota = ResourceQuota(
            user_id=user.id,
            max_cpu_cores=0, max_ram_gb=0, max_traffic_gb=0,
            max_public_ips=0, max_disk_gb=0,
        )
        db.add(quota)

    await db.commit()
    await db.refresh(consumption)
    await db.refresh(quota)

    return UserResourceInfo(
        consumption=_consumption_to_schema(consumption, user_uuid),
        quota=_quota_to_schema(quota, user_uuid),
    )

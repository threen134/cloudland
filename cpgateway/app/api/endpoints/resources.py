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


@router.get("/consumption/{user_uuid}", response_model=ResourceConsumptionSchema)
async def get_user_consumption(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    """
    【资源统计】获取用户当前已实际分配的云资源总量。
    若该用户尚无统计记录，则自动初始化为全 0。
    """
    # Check if user exists
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"User with UUID {user_uuid} not found"
        )
    
    user_id = user.id
    # Get resource consumption
    result = await db.execute(
        select(ResourceConsumption).where(ResourceConsumption.user_id == user_id)
    )
    consumption = result.scalars().first()
    
    if not consumption:
        # Create default consumption record if it doesn't exist
        consumption = ResourceConsumption(
            user_id=user_id,
            cpu_cores=0.0,
            ram_gb=0.0,
            traffic_gb=0.0,
            public_ips=0,
            disk_gb=0.0
        )
        db.add(consumption)
        await db.commit()
        await db.refresh(consumption)
    
    return consumption


@router.get("/quota/{user_uuid}", response_model=ResourceQuotaSchema)
async def get_user_quota(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    """
    【配额管理】获取用户的最大允许资源可用限额。
    若无记录，则根据配置文件中的系统默认值进行初始化。
    """
    # Check if user exists
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"User with UUID {user_uuid} not found"
        )
    
    user_id = user.id
    # Get resource quota
    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user_id)
    )
    quota = result.scalars().first()
    
    if not quota:
        # Create default quota with all 0s (admin must grant quota)
        quota = ResourceQuota(
            user_id=user_id,
            max_cpu_cores=0,
            max_ram_gb=0,
            max_traffic_gb=0,
            max_public_ips=0,
            max_disk_gb=0,
        )
        db.add(quota)
        await db.commit()
        await db.refresh(quota)

    return quota


@router.put("/quota/{user_uuid}", response_model=ResourceQuotaSchema)
async def update_user_quota(
    user_uuid: str,
    quota_update: ResourceQuotaUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """
    【配额更新】手动调整用户的资源限制（仅 SystemAdmin）。
    支持按字段按需更新。
    """
    # Check if user exists
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"User with UUID {user_uuid} not found"
        )

    user_id = user.id
    # Get or create quota
    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user_id)
    )
    quota = result.scalars().first()

    if not quota:
        # Create new quota with all 0s, then update
        quota = ResourceQuota(
            user_id=user_id,
            max_cpu_cores=0,
            max_ram_gb=0,
            max_traffic_gb=0,
            max_public_ips=0,
            max_disk_gb=0,
        )
        db.add(quota)
    
    # Update only provided fields
    update_data = quota_update.model_dump(exclude_unset=True)
    for field, value in update_data.items():
        setattr(quota, field, value)
    
    await db.commit()
    await db.refresh(quota)
    
    return quota


@router.get("/info/{user_uuid}", response_model=UserResourceInfo)
async def get_user_resource_info(
    user_uuid: str,
    db: AsyncSession = Depends(get_db)
):
    """
    【综合视图】一次性获取用户的配额与实际消耗情况，常用于前端 Dashboard 展示。
    """
    # Check if user exists
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"User with UUID {user_uuid} not found"
        )
    
    user_id = user.id
    # Get consumption
    result = await db.execute(
        select(ResourceConsumption).where(ResourceConsumption.user_id == user_id)
    )
    consumption = result.scalars().first()
    
    if not consumption:
        consumption = ResourceConsumption(
            user_id=user_id,
            cpu_cores=0.0,
            ram_gb=0.0,
            traffic_gb=0.0,
            public_ips=0,
            disk_gb=0.0
        )
        db.add(consumption)
    
    # Get quota
    result = await db.execute(
        select(ResourceQuota).where(ResourceQuota.user_id == user_id)
    )
    quota = result.scalars().first()
    
    if not quota:
        quota = ResourceQuota(
            user_id=user_id,
            max_cpu_cores=0,
            max_ram_gb=0,
            max_traffic_gb=0,
            max_public_ips=0,
            max_disk_gb=0,
        )
        db.add(quota)
    
    await db.commit()
    await db.refresh(consumption)
    await db.refresh(quota)
    
    return UserResourceInfo(consumption=consumption, quota=quota)

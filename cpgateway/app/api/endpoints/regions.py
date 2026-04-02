import re
import secrets
from fastapi import APIRouter, Depends, HTTPException, status, BackgroundTasks
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import func, delete
from sqlalchemy.future import select
from typing import List

from app.core.database import get_db, AsyncSessionLocal
from app.api.deps import get_current_superuser
from app.models.user import User
from app.models.region import Region
from app.schemas.region import (
    RegionCreate, RegionUpdate,
    RegionPublic, RegionAdmin, RegionCreated, RegionSecretRotated,
)
from app.models.org import Organization
from app.models.org_resource_quota import OrgResourceQuota
from app.models.org_resource_consumption import OrgResourceConsumption
from app.core.config import settings as app_settings
from app.services.settings_service import settings_service
from app.core.logging_config import logger

router = APIRouter()

REGION_NAME_PATTERN = re.compile(r'^[a-z0-9][a-z0-9-]*[a-z0-9]$')


def _generate_secret(length: int = 64) -> str:
    return secrets.token_urlsafe(length)


async def _get_region_or_404(db: AsyncSession, region_uuid: str) -> Region:
    result = await db.execute(select(Region).where(Region.uuid == region_uuid))
    region = result.scalars().first()
    if not region:
        raise HTTPException(status_code=404, detail="Region not found")
    return region


# --- Region CRUD ---

@router.post("", response_model=RegionCreated, status_code=status.HTTP_201_CREATED)
async def create_region(
    region_in: RegionCreate,
    background_tasks: BackgroundTasks,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """注册新 Region（仅 SystemAdmin）。internal_secret 仅在创建时返回一次。"""
    if not REGION_NAME_PATTERN.match(region_in.name):
        raise HTTPException(status_code=400, detail="Region name must match [a-z0-9-], min 2 chars")

    result = await db.execute(select(Region).where(Region.name == region_in.name))
    if result.scalars().first():
        raise HTTPException(status_code=400, detail=f"Region '{region_in.name}' already exists")

    secret = region_in.internal_secret
    if secret == "auto-generate":
        secret = _generate_secret()

    region = Region(
        name=region_in.name,
        display_name=region_in.display_name or region_in.name,
        internal_endpoint=region_in.internal_endpoint.strip(),
        internal_secret=secret,
        is_available=False,
        description=region_in.description,
    )
    db.add(region)
    await db.flush()  # Get region.id without committing

    # 从动态设置读取配额默认值（三级降级：DB → 环境变量 → 硬编码默认）
    default_cpu = int(await settings_service.get(db, "DEFAULT_CPU_CORES") or app_settings.DEFAULT_CPU_CORES)
    default_ram = int(await settings_service.get(db, "DEFAULT_RAM_GB") or app_settings.DEFAULT_RAM_GB)
    default_ips = int(await settings_service.get(db, "DEFAULT_PUBLIC_IPS") or app_settings.DEFAULT_PUBLIC_IPS)
    default_disk = int(await settings_service.get(db, "DEFAULT_DISK_GB") or app_settings.DEFAULT_DISK_GB)

    # Initialize quota/consumption for all existing orgs in this new region
    orgs_result = await db.execute(
        select(Organization).where(Organization.deleted_at.is_(None))
    )
    for org in orgs_result.scalars().all():
        db.add(OrgResourceQuota(
            org_id=org.id,
            region_id=region.id,
            max_cpu_cores=default_cpu,
            max_ram_gb=default_ram,
            max_public_ips=default_ips,
            max_disk_gb=default_disk,
        ))
        db.add(OrgResourceConsumption(
            org_id=org.id,
            region_id=region.id,
        ))

    # Single atomic commit: region + all org quota/consumption records
    await db.commit()
    await db.refresh(region)

    logger.info(f"Region '{region.name}' created by {current_user.username}")

    # 新区域注册后异步 Provisioning：推送通知渠道和系统设置
    background_tasks.add_task(_provision_new_region, region.id, region.name)

    return region


async def _provision_new_region(region_id: int, region_name: str):
    """新区域注册后，异步推送全量通知渠道和系统设置"""
    try:
        from app.services.notification_service import notification_sync_service
        from app.services.settings_sync_service import settings_sync_service
        async with AsyncSessionLocal() as db:
            res = await db.execute(select(Region).where(Region.id == region_id))
            r = res.scalars().first()
            if r:
                await notification_sync_service.push_all_channels_to_region(db, r)
                logger.info(f"Provisioned notification channels to new region '{region_name}'")
                await settings_sync_service.push_settings_to_region(db, r)
                logger.info(f"Provisioned system settings to new region '{region_name}'")
    except Exception as e:
        logger.error(f"Failed to provision new region '{region_name}': {e}")


@router.get("", response_model=List[RegionPublic])
async def list_regions(
    skip: int = 0,
    limit: int = 100,
    db: AsyncSession = Depends(get_db),
):
    """列出所有 Region（公开接口，不暴露内网地址和密钥）"""
    result = await db.execute(select(Region).offset(skip).limit(limit))
    return result.scalars().all()


@router.get("/{region_uuid}", response_model=RegionAdmin)
async def get_region(
    region_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """Region 详情（仅 SystemAdmin，含内网地址，不含密钥）"""
    return await _get_region_or_404(db, region_uuid)


@router.patch("/{region_uuid}", response_model=RegionAdmin)
async def update_region(
    region_uuid: str,
    region_in: RegionUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """更新 Region（仅 SystemAdmin）"""
    region = await _get_region_or_404(db, region_uuid)

    update_data = region_in.model_dump(exclude_unset=True)
    for field, value in update_data.items():
        if field == "internal_endpoint" and value:
            value = value.strip()
        setattr(region, field, value)

    # 进入维护模式时同步下线，避免请求继续转发到该 Region
    if update_data.get("maintenance_mode") is True:
        region.is_available = False
    # 手动上线时自动退出维护模式，防止心跳永久跳过该 Region
    elif update_data.get("is_available") is True:
        region.maintenance_mode = False

    await db.commit()
    await db.refresh(region)

    logger.info(f"Region '{region.name}' updated by {current_user.username}")
    return region


@router.delete("/{region_uuid}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_region(
    region_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """
    删除 Region (Delete Region)
    
    安全保护：如果该 Region 下还有活跃的云服务器、云硬盘等资源，为了避免资源孤儿，必须先清空资源。
    """
    region = await _get_region_or_404(db, region_uuid)

    # 1. 安全保护：必须处于维护模式才能删除 (Must be in maintenance mode)
    if not region.maintenance_mode:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=(
                f"Cannot delete region '{region.name}': "
                "The region must be put into maintenance mode first."
            )
        )

    # 2. 安全保护：检查整个 Region 的资源消费总量 (Check for any active resources)
    # 只有当该 Region 在所有 Org 下的已用资源均为 0 时，才允许删除
    usage_result = await db.execute(
        select(
            func.sum(OrgResourceConsumption.cpu_cores),
            func.sum(OrgResourceConsumption.ram_gb),
            func.sum(OrgResourceConsumption.disk_gb),
            func.sum(OrgResourceConsumption.public_ips)
        ).where(OrgResourceConsumption.region_id == region.id)
    )
    row = usage_result.one()
    if any(float(v or 0) > 0 for v in row):
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=(
                f"Cannot delete region '{region.name}': "
                "There are still active resources (VMs, volumes, or floating IPs). "
                "Please delete all resources in this region first."
            )
        )

    # 3. 清理网关侧的关联记录 (Clean up CPGateway side records)
    await db.execute(delete(OrgResourceQuota).where(OrgResourceQuota.region_id == region.id))
    await db.execute(delete(OrgResourceConsumption).where(OrgResourceConsumption.region_id == region.id))

    # 4. 删除 Region 本身
    await db.delete(region)
    await db.commit()
    logger.info(f"Region '{region.name}' deleted by {current_user.username}")


@router.post("/{region_uuid}/rotate-secret", response_model=RegionSecretRotated)
async def rotate_secret(
    region_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """轮换 Region 密钥（仅 SystemAdmin）。返回新密钥，需同步更新 Cloudland 配置。"""
    region = await _get_region_or_404(db, region_uuid)

    new_secret = _generate_secret()
    region.internal_secret = new_secret
    await db.commit()

    logger.info(f"Region '{region.name}' secret rotated by {current_user.username}")
    return RegionSecretRotated(
        region_uuid=region.uuid,
        name=region.name,
        new_secret=new_secret,
    )

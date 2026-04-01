import asyncio
from fastapi import APIRouter, Depends, HTTPException, status, BackgroundTasks
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from sqlalchemy import func
from typing import List

from app.core.database import get_db, AsyncSessionLocal
from app.api.deps import get_current_active_user, get_current_org
from app.models.user import User
from app.models.org import Organization
from app.models.notification import NotificationChannel
from app.schemas.notification import (
    ChannelCreate, ChannelUpdate, ChannelResponse, ChannelListResponse,
)
from app.services.notification_service import notification_sync_service
from app.core.logging_config import logger

router = APIRouter()


@router.get("", response_model=ChannelListResponse)
async def list_channels(
    db: AsyncSession = Depends(get_db),
    current_org: Organization = Depends(get_current_org),
):
    """列出当前组织的所有通知渠道"""
    count_result = await db.execute(
        select(func.count(NotificationChannel.id)).where(
            NotificationChannel.org_id == current_org.id
        )
    )
    total = count_result.scalar() or 0

    result = await db.execute(
        select(NotificationChannel)
        .where(NotificationChannel.org_id == current_org.id)
        .order_by(NotificationChannel.created_at.desc())
    )
    channels = result.scalars().all()

    return ChannelListResponse(
        total=total,
        channels=[ChannelResponse.model_validate(ch) for ch in channels],
    )


@router.post("", response_model=ChannelResponse, status_code=status.HTTP_201_CREATED)
async def create_channel(
    channel_in: ChannelCreate,
    background_tasks: BackgroundTasks,
    db: AsyncSession = Depends(get_db),
    current_org: Organization = Depends(get_current_org),
):
    """创建通知渠道，创建后异步推送到所有 Region"""
    channel = NotificationChannel(
        org_id=current_org.id,
        name=channel_in.name,
        type=channel_in.type,
        config=channel_in.config,
        enabled=channel_in.enabled,
    )
    db.add(channel)
    await db.commit()
    await db.refresh(channel)

    logger.info(f"Org {current_org.id} created notification channel '{channel.name}' ({channel.uuid})")

    # 后台异步推送到所有 Region
    background_tasks.add_task(_sync_channel_to_regions, "upsert", channel.uuid)

    return ChannelResponse.model_validate(channel)


@router.get("/{channel_uuid}", response_model=ChannelResponse)
async def get_channel(
    channel_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_org: Organization = Depends(get_current_org),
):
    """获取通知渠道详情"""
    channel = await _get_org_channel(db, channel_uuid, current_org.id)
    return ChannelResponse.model_validate(channel)


@router.put("/{channel_uuid}", response_model=ChannelResponse)
async def update_channel(
    channel_uuid: str,
    channel_in: ChannelUpdate,
    background_tasks: BackgroundTasks,
    db: AsyncSession = Depends(get_db),
    current_org: Organization = Depends(get_current_org),
):
    """更新通知渠道，更新后异步推送到所有 Region"""
    channel = await _get_org_channel(db, channel_uuid, current_org.id)

    update_data = channel_in.model_dump(exclude_unset=True)
    for field, value in update_data.items():
        setattr(channel, field, value)

    await db.commit()
    await db.refresh(channel)

    logger.info(f"Org {current_org.id} updated notification channel '{channel.name}' ({channel.uuid})")

    background_tasks.add_task(_sync_channel_to_regions, "upsert", channel.uuid)

    return ChannelResponse.model_validate(channel)


@router.delete("/{channel_uuid}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_channel(
    channel_uuid: str,
    background_tasks: BackgroundTasks,
    db: AsyncSession = Depends(get_db),
    current_org: Organization = Depends(get_current_org),
):
    """删除通知渠道，删除后异步推送到所有 Region"""
    channel = await _get_org_channel(db, channel_uuid, current_org.id)

    await db.delete(channel)
    await db.commit()

    logger.info(f"Org {current_org.id} deleted notification channel ({channel_uuid})")

    background_tasks.add_task(_sync_channel_to_regions, "delete", channel_uuid)


# --- Helpers ---

async def _get_org_channel(
    db: AsyncSession, channel_uuid: str, org_id: int
) -> NotificationChannel:
    result = await db.execute(
        select(NotificationChannel).where(
            NotificationChannel.uuid == channel_uuid,
            NotificationChannel.org_id == org_id,
        )
    )
    channel = result.scalars().first()
    if not channel:
        raise HTTPException(status_code=404, detail="Notification channel not found")
    return channel


async def _sync_channel_to_regions(action: str, channel_uuid: str):
    """后台任务：推送渠道变更到所有 Region"""
    async with AsyncSessionLocal() as db:
        if action == "upsert":
            result = await db.execute(
                select(NotificationChannel).where(
                    NotificationChannel.uuid == channel_uuid
                )
            )
            channel = result.scalars().first()
            if channel:
                await notification_sync_service.push_channel_to_all_regions(
                    db, action="upsert", channel=channel
                )
        elif action == "delete":
            await notification_sync_service.push_channel_to_all_regions(
                db, action="delete", channel_uuid=channel_uuid
            )

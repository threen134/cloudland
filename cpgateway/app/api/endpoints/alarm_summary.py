import asyncio
from fastapi import APIRouter, Depends
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.models.region import Region
from app.schemas.notification import AlarmSummaryResponse, AlarmSummaryRegion
from app.services.notification_service import notification_sync_service

router = APIRouter(tags=["Alarm"])


@router.get("/alarm/summary", response_model=AlarmSummaryResponse)
async def get_alarm_summary(
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """
    全局告警汇总：并发查询所有活跃 Region 的 firing 告警数。
    单个 Region 超时（2s）时返回 firing_count=-1，前端可展示"不可达"。
    """
    result = await db.execute(
        select(Region).where(
            Region.is_available.is_(True),
            Region.maintenance_mode.is_(False),
        )
    )
    regions = result.scalars().all()

    if not regions:
        return AlarmSummaryResponse(regions=[], total_firing=0)

    tasks = [
        notification_sync_service.get_alarm_firing_count(region)
        for region in regions
    ]
    try:
        counts = await asyncio.wait_for(asyncio.gather(*tasks), timeout=10.0)
    except asyncio.TimeoutError:
        counts = [-1] * len(regions)

    region_summaries = []
    total_firing = 0
    for region, count in zip(regions, counts):
        region_summaries.append(
            AlarmSummaryRegion(
                region_uuid=region.uuid,
                region_name=region.display_name or region.name,
                firing_count=count,
            )
        )
        if count > 0:
            total_firing += count

    return AlarmSummaryResponse(regions=region_summaries, total_firing=total_firing)

from fastapi import APIRouter, Depends, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['alarm'])

@router.post("/api/v1/metrics/alarm/sync-mappings", summary="Synchronize all VM rule mappings")
async def post_api_v1_metrics_alarm_sync_mappings(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    Perform a full synchronization of all VM rule mappings to ensure matched_vms.json is consistent with the database
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/api/v1/metrics/alarm/sync-mappings"
    )

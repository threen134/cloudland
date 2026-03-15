from fastapi import APIRouter, Depends, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Administration'])

@router.get("/hypers", summary="list hypervisors")
async def get_hypers(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list hypervisors
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/hypers"
    )

@router.get("/hypers/{hostid}", summary="get a hypervisor")
async def get_hypers_hostid(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a hypervisor
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/hypers/{hostid}"
    )

@router.patch("/hypers/{hostid}", summary="update a hypervisor")
async def patch_hypers_hostid(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    update hypervisor status, zone, over-commit rates, and remark
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/hypers/{hostid}"
    )

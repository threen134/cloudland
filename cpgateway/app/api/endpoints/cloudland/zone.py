from fastapi import APIRouter, Depends, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Zone'])

@router.get("/zones", summary="list zones")
async def get_zones(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list zones
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/zones"
    )

@router.post("/zones", summary="create a zone")
async def post_zones(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a zone
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/zones"
    )

@router.get("/zones/{name}", summary="get a zone")
async def get_zones_name(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a zone
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/zones/{name}"
    )

@router.delete("/zones/{name}", summary="delete a zone")
async def delete_zones_name(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a zone
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/zones/{name}"
    )

@router.patch("/zones/{name}", summary="patch a zone")
async def patch_zones_name(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a zone
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/zones/{name}"
    )

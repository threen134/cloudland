from fastapi import APIRouter, Depends, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Authorization'])


@router.get("/keys", summary="list keys")
async def get_keys(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list keys
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/keys"
    )

@router.post("/keys", summary="create a key")
async def post_keys(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/keys"
    )

@router.get("/keys/{id}", summary="get a key")
async def get_keys_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/keys/{id}"
    )

@router.delete("/keys/{id}", summary="delete a key")
async def delete_keys_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/keys/{id}"
    )

@router.patch("/keys/{id}", summary="patch a key")
async def patch_keys_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/keys/{id}"
    )


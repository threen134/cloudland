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
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list keys
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/keys"
    )

@router.post("/keys", summary="create a key")
async def post_keys(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/keys"
    )

@router.get("/keys/{id}", summary="get a key")
async def get_keys_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/keys/{id}"
    )

@router.delete("/keys/{id}", summary="delete a key")
async def delete_keys_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/keys/{id}"
    )

@router.patch("/keys/{id}", summary="patch a key")
async def patch_keys_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a key
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/keys/{id}"
    )

@router.post("/login", summary="login to get the access token")
async def post_login(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get token by user name
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/login"
    )

@router.get("/orgs", summary="list orgs")
async def get_orgs(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list orgs
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/orgs"
    )

@router.post("/orgs", summary="create a org")
async def post_orgs(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a org
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/orgs"
    )

@router.get("/orgs/{id}", summary="get a org")
async def get_orgs_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a org
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/orgs/{id}"
    )

@router.delete("/orgs/{id}", summary="delete a org")
async def delete_orgs_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a org
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/orgs/{id}"
    )

@router.patch("/orgs/{id}", summary="patch a org")
async def patch_orgs_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a org
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/orgs/{id}"
    )

@router.get("/users", summary="list users")
async def get_users(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list users
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/users"
    )

@router.post("/users", summary="create a user")
async def post_users(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a user
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/users"
    )

@router.get("/users/{id}", summary="get a user")
async def get_users_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a user
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/users/{id}"
    )

@router.delete("/users/{id}", summary="delete a user")
async def delete_users_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a user
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/users/{id}"
    )

@router.patch("/users/{id}", summary="patch a user")
async def patch_users_id(
    request: Request,
    region: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a user
    """
    return await proxy_service.forward_to_region(
        request=request,
        region_name=region,
        db=db,
        current_user=current_user,
        proxy_path="/users/{id}"
    )

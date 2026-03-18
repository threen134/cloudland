from fastapi import APIRouter, Depends, Request
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_superuser
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Administration'])

@router.get("/hypers", summary="list hypervisors")
async def get_hypers(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    list hypervisors (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers"
    )

@router.get("/hypers/{hostid}", summary="get a hypervisor")
async def get_hypers_hostid(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    get a hypervisor (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers/{hostid}"
    )

@router.patch("/hypers/{hostid}", summary="update a hypervisor")
async def patch_hypers_hostid(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    update hypervisor status, zone, over-commit rates, and remark
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers/{hostid}"
    )

@router.post("/hypers", summary="deploy a new hypervisor")
async def deploy_hyper(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    deploy a new compute node (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers"
    )

@router.post("/hypers/{hostid}/maintain", summary="maintain a hypervisor")
async def maintain_hyper(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    start maintenance for a hypervisor, optionally migrating all instances (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers/{hostid}/maintain"
    )

@router.delete("/hypers/{hostid}", summary="delete a hypervisor")
async def delete_hyper(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    remove a hypervisor record (requires superuser, no running instances allowed)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/hypers/{hostid}"
    )

@router.get("/node-alarm-rules", summary="list node alarm rules")
async def list_node_alarm_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    list node alarm rules, optionally filtered by uuid or rule_type (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/node-alarm-rules"
    )

@router.post("/node-alarm-rules", summary="create a node alarm rule")
async def create_node_alarm_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    create a node alarm rule (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/node-alarm-rules"
    )

@router.delete("/node-alarm-rules/{uuid}", summary="delete a node alarm rule")
async def delete_node_alarm_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser)
):
    """
    delete a node alarm rule (requires superuser)
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/node-alarm-rules/{uuid}"
    )

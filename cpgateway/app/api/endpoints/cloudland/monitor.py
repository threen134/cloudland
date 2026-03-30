from fastapi import APIRouter, Depends, Request
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=["Monitoring"])


# --- 实例历史指标查询（透传到 Region clapi）---

@router.post("/metrics/instances/cpu/his_data", summary="Query instance CPU history")
async def get_cpu_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/cpu/his_data",
    )


@router.post("/metrics/instances/memory/his_data", summary="Query instance memory history")
async def get_memory_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/memory/his_data",
    )


@router.post("/metrics/instances/disk/his_data", summary="Query instance disk history")
async def get_disk_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/disk/his_data",
    )


@router.post("/metrics/instances/network/his_data", summary="Query instance network history")
async def get_network_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/network/his_data",
    )


@router.post("/metrics/instances/traffic/his_data", summary="Query instance traffic history")
async def get_traffic_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/traffic/his_data",
    )


@router.post("/metrics/instances/volume/his_data", summary="Query instance volume history")
async def get_volume_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/instances/volume/his_data",
    )


# --- 宿主机历史指标查询（透传到 Region clapi）---

@router.post("/metrics/hypers/cpu/his_data", summary="Query hypervisor CPU history")
async def get_hyper_cpu_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/hypers/cpu/his_data",
    )


@router.post("/metrics/hypers/memory/his_data", summary="Query hypervisor memory history")
async def get_hyper_memory_history(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/metrics/hypers/memory/his_data",
    )

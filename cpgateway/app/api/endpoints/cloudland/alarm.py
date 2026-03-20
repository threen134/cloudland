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


# --- 告警事件查询（透传到 Region clapi）---

@router.get("/alarm/events", summary="List alarm events in current region")
async def list_alarm_events(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """按当前 Region 透传代理，获取告警事件列表（支持 status/page/page_size 参数）"""
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/api/v1/alarm/events",
    )


@router.get("/alarm/events/{event_uuid}/delivery-logs", summary="Get delivery logs for an alarm event")
async def get_alarm_delivery_logs(
    event_uuid: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """按当前 Region 透传代理，获取单条告警的发送流水"""
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path=f"/api/v1/alarm/events/{event_uuid}/delivery-logs",
    )


# --- 告警规则-渠道绑定（透传到 Region clapi）---

@router.post("/alarm/rule-channels", summary="Bind notification channels to an alarm rule")
async def bind_rule_channels(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """将通知渠道绑定到告警规则（透传到 Region clapi）"""
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/api/v1/alarm/rule-channels",
    )


@router.get("/alarm/rule-channels/{rule_group_uuid}", summary="Get channels bound to an alarm rule")
async def get_rule_channels(
    rule_group_uuid: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """获取告警规则绑定的通知渠道（透传到 Region clapi）"""
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path=f"/api/v1/alarm/rule-channels/{rule_group_uuid}",
    )


# --- VM 告警规则管理（透传到 Region clapi）---

@router.post("/metrics/alarm/cpu/rules", summary="Create a CPU alarm rule")
async def create_cpu_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/cpu/rules",
    )


@router.get("/metrics/alarm/cpu/rules", summary="List CPU alarm rules")
async def get_cpu_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/cpu/rules",
    )


@router.delete("/metrics/alarm/cpu/rule/{uuid}", summary="Delete a CPU alarm rule")
async def delete_cpu_rule(
    uuid: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path=f"/api/v1/metrics/alarm/cpu/rule/{uuid}",
    )


@router.post("/metrics/alarm/memory/rules", summary="Create a memory alarm rule")
async def create_memory_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/memory/rules",
    )


@router.get("/metrics/alarm/memory/rules", summary="List memory alarm rules")
async def get_memory_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/memory/rules",
    )


@router.delete("/metrics/alarm/memory/rule/{uuid}", summary="Delete a memory alarm rule")
async def delete_memory_rule(
    uuid: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path=f"/api/v1/metrics/alarm/memory/rule/{uuid}",
    )


@router.post("/metrics/alarm/bw/rules", summary="Create a bandwidth alarm rule")
async def create_bw_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/bw/rules",
    )


@router.get("/metrics/alarm/bw/rules", summary="List bandwidth alarm rules")
async def get_bw_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/bw/rules",
    )


@router.delete("/metrics/alarm/bw/rule/{uuid}", summary="Delete a bandwidth alarm rule")
async def delete_bw_rule(
    uuid: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path=f"/api/v1/metrics/alarm/bw/rule/{uuid}",
    )


@router.get("/metrics/alarm/active-rules", summary="Get active alarm rules for current user")
async def get_active_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/metrics/alarm/active-rules",
    )


@router.post("/metrics/alarm/link", summary="Link alarm rule to VM")
async def link_alarm_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/alarm/link",
    )


@router.post("/metrics/alarm/unlink", summary="Unlink alarm rule from VM")
async def unlink_alarm_rule(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    return await proxy_service.forward_to_region(
        request=request, db=db, proxy_path="/api/v1/alarm/unlink",
    )

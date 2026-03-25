"""
ConsumptionSyncService — 异步对账服务

在用户登录时后台触发，将 org_resource_consumptions 表与 Cloudland 后端实际资源对齐。

并发控制：用内存 set 记录正在同步的 (org_id, region_id) 对，防止同一 org-region 并发重复同步。
"""

import asyncio
import httpx
import time
from typing import Optional, Set, Tuple, Dict

from sqlalchemy.future import select

from app.core.database import AsyncSessionLocal
from app.core.logging_config import logger
from app.models.org_resource_consumption import OrgResourceConsumption
from app.services.quota_service import build_backend_url


# 正在同步的 (org_id, region_id) 对 — 进程内去重
_syncing: Set[Tuple[int, int]] = set()

# 上次成功同步的时间戳 {(org_id, region_id): timestamp}
_last_sync_time: Dict[Tuple[int, int], float] = {}


async def _fetch(client: httpx.AsyncClient, url: str, headers: dict) -> Optional[list]:
    """GET 一个资源列表，失败时返回 None 以区分'无资源'和'请求失败'。"""
    try:
        resp = await client.get(url, headers=headers, timeout=10.0)
        if resp.status_code != 200:
            logger.warning(f"ConsumptionSync: GET {url} -> {resp.status_code}")
            return None
        data = resp.json()
        if isinstance(data, list):
            return data
        # 兼容 {"instances": [...]} / {"volumes": [...]} 等包装格式
        for v in data.values():
            if isinstance(v, list):
                return v
        return []
    except Exception as e:
        logger.warning(f"ConsumptionSync: GET {url} error: {e}")
        return None


async def _do_sync(org_id: int, region_id: int, internal_endpoint: str, internal_secret: str):
    """查询后端资源，更新 consumption 表。"""
    headers = {
        "X-Org-ID": str(org_id),
        "X-Forwarded-Secret": internal_secret,
        "X-System-Role": "1",
    }
    base = build_backend_url(internal_endpoint)

    totals = {"cpu_cores": 0.0, "ram_gb": 0.0, "disk_gb": 0.0, "public_ips": 0}

    async with httpx.AsyncClient(verify=False) as client:
        # --- Instances (CPU + RAM) ---
        instances = await _fetch(client, f"{base}/instances", headers)
        if instances is None:
            raise RuntimeError(f"failed to fetch instances for org={org_id}")
        for inst in instances:
            totals["cpu_cores"] += float(inst.get("cpu", 0))
            totals["ram_gb"] += float(inst.get("memory", 0)) / 1024.0

        # --- Volumes (Disk) ---
        volumes = await _fetch(client, f"{base}/volumes", headers)
        if volumes is None:
            raise RuntimeError(f"failed to fetch volumes for org={org_id}")
        for vol in volumes:
            totals["disk_gb"] += float(vol.get("size", 0))

        # --- Floating IPs (Public IPs) ---
        fips = await _fetch(client, f"{base}/floating_ips", headers)
        if fips is None:
            raise RuntimeError(f"failed to fetch floating_ips for org={org_id}")
        totals["public_ips"] = len(fips)

    # 写入数据库（新建独立 session，避免与登录 session 冲突）
    async with AsyncSessionLocal() as db:
        result = await db.execute(
            select(OrgResourceConsumption)
            .where(
                OrgResourceConsumption.org_id == org_id,
                OrgResourceConsumption.region_id == region_id,
            )
            .with_for_update()
        )
        consumption = result.scalars().first()
        if not consumption:
            raise RuntimeError(f"no consumption record for org={org_id}, region={region_id}")

        consumption.cpu_cores = totals["cpu_cores"]
        consumption.ram_gb = totals["ram_gb"]
        consumption.disk_gb = totals["disk_gb"]
        consumption.public_ips = totals["public_ips"]
        await db.commit()

    logger.info(
        f"ConsumptionSync: org={org_id}, region={region_id} -> "
        f"cpu={totals['cpu_cores']}, ram={totals['ram_gb']:.2f}GB, "
        f"disk={totals['disk_gb']}GB, public_ips={totals['public_ips']}"
    )


async def _sync_task(org_id: int, region_id: int, internal_endpoint: str, internal_secret: str):
    """后台任务包装：运行完成后移除 _syncing 标记，并纪录成功同步时间。"""
    key = (org_id, region_id)
    try:
        await _do_sync(org_id, region_id, internal_endpoint, internal_secret)
        _last_sync_time[key] = time.time()
    except Exception as e:
        logger.error(f"ConsumptionSync failed: org={org_id}, region={region_id}: {e}")
    finally:
        _syncing.discard(key)


def trigger_sync(org_id: int, region_id: int, internal_endpoint: str, internal_secret: str):
    """
    登录时调用：若该 org-region 在 1 小时内未同步过，则启动后台对账任务。
    非阻塞，立即返回。
    """
    key = (org_id, region_id)
    if key in _syncing:
        logger.debug(f"ConsumptionSync: already in progress for org={org_id}, region={region_id}, skipping")
        return

    # 1 小时内不重复同步 (3600 秒)
    now = time.time()
    last_sync = _last_sync_time.get(key, 0)
    if now - last_sync < 3600:
        logger.debug(f"ConsumptionSync: skipped (last sync was {now - last_sync:.0f}s ago)")
        return

    _syncing.add(key)
    asyncio.create_task(_sync_task(org_id, region_id, internal_endpoint, internal_secret))
    logger.debug(f"ConsumptionSync: triggered for org={org_id}, region={region_id}")

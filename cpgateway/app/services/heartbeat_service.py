import httpx
import asyncio
from datetime import datetime, timezone
from sqlalchemy.future import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models.region import Region
from app.core.database import AsyncSessionLocal
from app.core.config import settings
from app.core.logging_config import logger

class HeartbeatService:
    """
    Region 心跳探测服务。
    定期对所有 Region 发起拨测，更新其可用性状态。
    """

    @staticmethod
    async def check_region_health(region: Region, db: AsyncSession):
        """
        探测单个 Region 的健康状况。
        请求 {endpoint}/api/v1/version (无需认证)。
        """
        if region.maintenance_mode:
            logger.debug(f"Region '{region.name}' is in maintenance mode, skipping heartbeat.")
            return

        # 兼容处理 endpoint 结尾的斜行
        base_url = region.internal_endpoint.rstrip("/")
        health_url = f"{base_url}/api/v1/version"
        
        start_time = datetime.now(timezone.utc)
        error_msg = None
        is_success = False

        try:
            # verify=False: internal_endpoint 为内网地址，无需验证 TLS 证书
            async with httpx.AsyncClient(verify=False, timeout=settings.REGION_HEARTBEAT_TIMEOUT) as client:
                response = await client.get(health_url)
                if response.status_code == 200:
                    is_success = True
                else:
                    error_msg = f"HTTP {response.status_code}"
        except httpx.RequestError as e:
            # 包含 httpx.TimeoutException 等所有网络/超时异常
            error_msg = f"Network error: {str(e)}"
        except Exception as e:
            error_msg = f"Unexpected error: {str(e)}"

        # 更新数据库状态
        region.last_check_at = start_time
        
        if is_success:
            # 自动恢复逻辑：成功一次即上线
            was_unavailable = not region.is_available
            if was_unavailable:
                logger.info(f"Region '{region.name}' heartbeats recovered. Marking as available.")

            region.is_available = True
            region.fail_count = 0
            region.status_message = "Healthy"

            # cold_start 补偿：Region 刚恢复时异步触发全量通知渠道推送
            # 使用 create_task 避免阻塞心跳事务的 commit
            if was_unavailable:
                async def _cold_start_sync(region_id, region_name):
                    try:
                        from app.services.notification_service import notification_sync_service
                        async with AsyncSessionLocal() as sync_db:
                            res = await sync_db.execute(
                                select(Region).where(Region.id == region_id)
                            )
                            r = res.scalars().first()
                            if r:
                                await notification_sync_service.push_all_channels_to_region(sync_db, r)
                                logger.info(f"Cold start channel sync triggered for region '{region_name}'")
                    except Exception as e:
                        logger.error(f"Cold start channel sync failed for region '{region_name}': {e}")

                asyncio.create_task(_cold_start_sync(region.id, region.name))
        else:
            # 故障逻辑：连续失败达到阈值才下线
            region.fail_count += 1
            region.status_message = error_msg
            
            if region.fail_count >= settings.REGION_HEARTBEAT_OFFLINE_THRESHOLD:
                if region.is_available:
                    logger.warning(
                        f"Region '{region.name}' failed {region.fail_count} times. "
                        f"Marking as unavailable. Reason: {error_msg}"
                    )
                region.is_available = False
            else:
                logger.debug(f"Region '{region.name}' check failed ({region.fail_count}/{settings.REGION_HEARTBEAT_OFFLINE_THRESHOLD}): {error_msg}")

    @classmethod
    async def check_all_regions(cls):
        """遍历所有非维护模式的 Region 并执行拨测"""
        async with AsyncSessionLocal() as db:
            result = await db.execute(
                select(Region).where(Region.maintenance_mode.is_(False))
            )
            regions = result.scalars().all()
            
            if not regions:
                return

            tasks = [cls.check_region_health(region, db) for region in regions]
            await asyncio.gather(*tasks)
            await db.commit()

async def heartbeat_loop():
    """后台无限循环任务"""
    logger.info("Starting Region heartbeat background task...")
    # 启动时先等几秒，避开应用初始化高峰
    await asyncio.sleep(5)
    
    while True:
        try:
            await HeartbeatService.check_all_regions()
        except Exception as e:
            logger.error(f"Error in heartbeat loop: {e}")
        
        await asyncio.sleep(settings.REGION_HEARTBEAT_INTERVAL)


import asyncio
from unittest.mock import AsyncMock, MagicMock, patch
from datetime import datetime, timezone
from app.models.region import Region
from app.services.heartbeat_service import HeartbeatService
from app.core.config import settings


def _make_http_mock(status_code: int):
    """构造一个模拟 httpx.AsyncClient 异步上下文管理器，返回指定状态码。"""
    mock_response = MagicMock(status_code=status_code)
    mock_client = AsyncMock()
    mock_client.get = AsyncMock(return_value=mock_response)
    mock_cm = MagicMock()
    mock_cm.__aenter__ = AsyncMock(return_value=mock_client)
    mock_cm.__aexit__ = AsyncMock(return_value=False)
    return mock_cm


async def test_heartbeat_logic():
    # 1. Setup mock region
    region = Region(
        name="test-region",
        internal_endpoint="http://mock-region",
        is_available=True,
        fail_count=0
    )
    db = AsyncMock()

    # 2. Test Success (Case: Online -> Stay Online)
    with patch("app.services.heartbeat_service.httpx.AsyncClient", return_value=_make_http_mock(200)):
        await HeartbeatService.check_region_health(region, db)

        assert region.is_available is True
        assert region.fail_count == 0
        assert region.status_message == "Healthy"
        assert region.last_check_at is not None

    # 3. Test Failure (Case: Online -> Fail 1)
    with patch("app.services.heartbeat_service.httpx.AsyncClient", return_value=_make_http_mock(500)):
        await HeartbeatService.check_region_health(region, db)

        assert region.is_available is True  # Still True because threshold is 3
        assert region.fail_count == 1
        assert region.status_message == "HTTP 500"

    # 4. Test Failure (Case: Fail 3 -> Offline)
    region.fail_count = 2  # Simulate already failed twice
    with patch("app.services.heartbeat_service.httpx.AsyncClient", return_value=_make_http_mock(503)):
        await HeartbeatService.check_region_health(region, db)

        assert region.is_available is False
        assert region.fail_count == 3
        assert region.status_message == "HTTP 503"

    # 5. Test Recovery (Case: Offline -> Online)
    with patch("app.services.heartbeat_service.httpx.AsyncClient", return_value=_make_http_mock(200)):
        await HeartbeatService.check_region_health(region, db)

        assert region.is_available is True
        assert region.fail_count == 0
        assert region.status_message == "Healthy"

    # 6. Test Maintenance Mode (heartbeat must not touch any field)
    region.maintenance_mode = True
    region.is_available = False   # Manually disabled
    region.fail_count = 99        # Sentinel: must remain unchanged
    region.status_message = "prior_state"  # Sentinel: must remain unchanged
    with patch("app.services.heartbeat_service.httpx.AsyncClient", return_value=_make_http_mock(200)):
        await HeartbeatService.check_region_health(region, db)

        assert region.is_available is False   # not restored
        assert region.fail_count == 99        # not touched
        assert region.status_message == "prior_state"  # not touched

    print("Heartbeat logic verification PASSED!")

if __name__ == "__main__":
    asyncio.run(test_heartbeat_logic())

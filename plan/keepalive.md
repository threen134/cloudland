# Region Heartbeat Mechanism Implementation Plan

Add a background heartbeat mechanism to [cpgateway](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/dockerfiles/Dockerfile.cpgateway) to automatically monitor the availability of each Region and update the `is_available` status in the database.

## Proposed Changes

### [Component] CPGateway Backend

#### [MODIFY] [region.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/models/region.py)
- Add `last_check_at` (DateTime) to track the last time a heartbeat was performed.
- Add `status_message` (String) to store error messages if a check fails.
- Add `fail_count` (Integer, default 0) to track consecutive failures.

#### [MODIFY] [region_schema.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/schemas/region.py)
- Update `RegionPublic` and `RegionAdmin` to include the new fields.

#### [MODIFY] [config.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/core/config.py)
- Add `REGION_HEARTBEAT_INTERVAL` (default: 60s).
- Add `REGION_HEARTBEAT_TIMEOUT` (default: 5s).
- Add `REGION_HEARTBEAT_OFFLINE_THRESHOLD` (default: 3).

#### [NEW] [heartbeat_service.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/services/heartbeat_service.py)
- Implement `HeartbeatService` with an async method to ping all regions.
- Use `httpx` to send a GET request to `{internal_endpoint}/version`.
- **Logic**:
  - **Success**: Reset `fail_count` to 0, update `last_check_at`, and set `is_available = True` (automatic recovery).
  - **Failure**: Increment `fail_count`. If `fail_count >= OFFLINE_THRESHOLD`, set `is_available = False`. Update `status_message` with error details.

#### [MODIFY] [main.py](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/main.py)
- Add a background task loop that runs `HeartbeatService.check_all_regions()` at the configured interval.
- Start this loop in the `@app.on_event("startup")` handler.

## Verification Plan

### Automated Tests
- Since this involves background tasks and external network calls, I will add a script `tests/test_heartbeat.py` to:
  1. Mock the `httpx` response.
  2. Call the heartbeat check logic.
  3. Verify the database state is updated correctly.

### Manual Verification
1. Start [cpgateway](file:///Users/spark/workspace/cloud/cloudland/deploy/docker/dockerfiles/Dockerfile.cpgateway) locally.
2. Add a mock region pointing to a local dummy server (or just an invalid URL).
3. Observe the logs to see the heartbeat task running.
4. Verify the `is_available` and `last_check_at` fields in the [regions](file:///Users/spark/workspace/cloud/cloudland/cpgateway/app/api/endpoints/regions.py#96-105) table (e.g., using `sqlite3 cloudland.db`).
5. Stop/Start the dummy server and verify the status flips accordingly.

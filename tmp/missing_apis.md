Total APIs found in Clapi: 177
Total APIs found in Cpgateway: 127
Number of APIs missing in Cpgateway: 66

### Missing APIs in Cpgateway (Categorized by Tag)

#### Address
- `GET /api/v1/addresses/{uuid}`

#### Administration
- `DELETE /api/v1/hypers/{uuid}`
- `GET /api/v1/hypers/{uuid}`
- `PATCH /api/v1/addresses/remark`
- `PATCH /api/v1/addresses/update-lock`
- `PATCH /api/v1/hypers/{uuid}`
- `POST /api/v1/hypers`
- `POST /api/v1/hypers/{uuid}/maintain`

#### Alarm
- `DELETE /api/v1/metrics/alarm/bw/rule/{uuid}`
- `DELETE /api/v1/metrics/alarm/cpu/rule/{uuid}`
- `DELETE /api/v1/metrics/alarm/memory/rule/{uuid}`
- `DELETE /api/v1/node-alarm-rules/{uuid}`
- `GET /api/v1/metrics/alarm/active-rules`
- `GET /api/v1/metrics/alarm/bw/rules`
- `GET /api/v1/metrics/alarm/cpu/rules`
- `GET /api/v1/metrics/alarm/memory/rules`
- `GET /api/v1/metrics/api/v1/current-alarms`
- `GET /api/v1/metrics/api/v1/history-alarms`
- `GET /api/v1/node-alarm-rules`
- `POST /api/v1/metrics/alarm/bw/rules`
- `POST /api/v1/metrics/alarm/cpu/rules`
- `POST /api/v1/metrics/alarm/memory/rules`
- `POST /api/v1/metrics/rules/batch`
- `POST /api/v1/node-alarm-rules`

#### Auto Scaling
- `DELETE /api/v1/metrics/adjust/bw/rule/{uuid}`
- `DELETE /api/v1/metrics/adjust/cpu/rule/{uuid}`
- `DELETE /api/v1/metrics/adjust/unlink`
- `GET /api/v1/metrics/adjust/bw/rules`
- `GET /api/v1/metrics/adjust/cpu/rules`
- `GET /api/v1/metrics/api/v1/rules/links`
- `PATCH /api/v1/metrics/adjust/bw/rule/{uuid}`
- `PATCH /api/v1/metrics/adjust/cpu/rule/{uuid}`
- `POST /api/v1/alerts/resource-adjustment`
- `POST /api/v1/metrics/adjust/bw/rules`
- `POST /api/v1/metrics/adjust/cpu/rules`
- `POST /api/v1/metrics/adjust/link`
- `POST /api/v1/metrics/api/v1/adjust/regenerate-bandwidth-metrics`

#### Consistency Group
- `DELETE /api/v1/consistency_groups/{id}`
- `DELETE /api/v1/consistency_groups/{id}/snapshots/{snap_id}`
- `DELETE /api/v1/consistency_groups/{id}/volumes/{volume_id}`
- `GET /api/v1/consistency_groups`
- `GET /api/v1/consistency_groups/{id}`
- `GET /api/v1/consistency_groups/{id}/snapshots`
- `GET /api/v1/consistency_groups/{id}/snapshots/{snap_id}`
- `PATCH /api/v1/consistency_groups/{id}`
- `POST /api/v1/consistency_groups`
- `POST /api/v1/consistency_groups/{id}/snapshots`
- `POST /api/v1/consistency_groups/{id}/snapshots/{snap_id}/restore`
- `POST /api/v1/consistency_groups/{id}/volumes`

#### Monitoring
- `POST /api/v1/metrics/instances/cpu/his_data`
- `POST /api/v1/metrics/instances/disk/his_data`
- `POST /api/v1/metrics/instances/memory/his_data`
- `POST /api/v1/metrics/instances/network/his_data`
- `POST /api/v1/metrics/instances/traffic/his_data`
- `POST /api/v1/metrics/instances/volume/his_data`

#### Notification
- `GET /api/v1/alarm/events`
- `GET /api/v1/alarm/events/{event_uuid}/delivery-logs`
- `GET /api/v1/alarm/rule-channels/{uuid}`
- `GET /api/v1/internal/alarm/events`
- `POST /api/v1/alarm/rule-channels`
- `POST /api/v1/alerts/process`
- `POST /api/v1/internal/notification-channels/sync`

#### OpenMeter
- `GET /api/v1/openmeter/metrics`
- `GET /api/v1/openmeter/metrics/{instance_id}/{subject}`
- `GET /api/v1/openmeter/subjects`

#### Security Group
- `PATCH /api/v1/security_groups/{id}/rules/{rule_id}`

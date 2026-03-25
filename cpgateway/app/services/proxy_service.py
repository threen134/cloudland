import httpx
from datetime import datetime, timezone
from fastapi import Request, Response, HTTPException
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.region import Region
from app.models.user import User
from app.models.org import Organization, OrgType
from app.models.token_revocation import TokenRevocation
from app.core.security import verify_access_token
from app.core.config import settings
from app.core.logging_config import logger
from app.services.quota_service import quota_service, build_backend_url


HOP_BY_HOP = frozenset([
    'connection', 'keep-alive', 'proxy-authenticate',
    'proxy-authorization', 'te', 'trailers',
    'transfer-encoding', 'upgrade', 'host',
    'content-length', 'authorization'
])

# 配额规则：哪些 API 路径 + HTTP 方法会影响配额
QUOTA_RULES = {
    ("POST", "instances"): "consume",
    ("POST", "volumes"): "consume",
    ("POST", "floating_ips"): "consume",
    ("DELETE", "instances"): "release",
    ("DELETE", "volumes"): "release",
    ("DELETE", "floating_ips"): "release",
    ("PUT", "instances"): "resize",
    ("PUT", "volumes"): "resize",
}


class ProxyService:
    """
    统一 API Gateway / Reverse Proxy。
    所有前端对 Cloudland 资源的请求统一经 CPGateway 转发。

    流程:
    1. 验签 JWT (RS256)
    2. 检查 jti 是否已被吊销
    3. 从 claims.region 查 regions 表，获取内网地址
    4. [新增] 配额检查与预扣 (CREATE/DELETE/RESIZE)
    5. 转发请求到 Cloudland 内网，携带 X-* Header
    6. 配额后处理 (成功释放/失败回滚)
    7. 透传 Cloudland 响应
    """

    @staticmethod
    def _match_quota_rule(method: str, proxy_path: str):
        """匹配配额规则，返回 action 或 None"""
        # proxy_path examples: "instances", "instances/123", "volumes", "volumes/456"
        path_parts = proxy_path.strip("/").split("/")
        if not path_parts:
            return None

        resource_type = path_parts[0]  # e.g. "instances", "volumes", "floating_ips"
        has_id = len(path_parts) >= 2  # e.g. "instances/123"

        if method == "POST" and not has_id:
            return QUOTA_RULES.get(("POST", resource_type))
        elif method == "DELETE" and has_id:
            return QUOTA_RULES.get(("DELETE", resource_type))
        elif method == "PUT" and has_id:
            return QUOTA_RULES.get(("PUT", resource_type))

        return None

    @staticmethod
    def _extract_resource_id(proxy_path: str) -> str:
        """从 path 中提取资源 ID，如 instances/abc-123 → abc-123"""
        parts = proxy_path.strip("/").split("/")
        if len(parts) >= 2:
            return parts[1]
        return ""

    @staticmethod
    async def _extract_resource_amount(
        request: Request, proxy_path: str, region: Region, headers: dict,
    ) -> dict:
        """
        从 CREATE 请求中提取资源需求量。
        - POST /instances: 提取 flavor_id，查 Cloudland 获取 cpu/ram/disk
        - POST /volumes: 直接从请求体取 size
        - POST /floating_ips: 固定返回 {"public_ips": 1}
        """
        body = await request.json()

        if "instances" in proxy_path:
            flavor_id = body.get("flavor")
            if flavor_id is None:
                return {}
            return await quota_service.query_flavor_amount(region, flavor_id, headers)

        elif "volumes" in proxy_path:
            size = body.get("size")
            if size is not None:
                return {"disk_gb": float(size)}
            return {}

        elif "floating_ips" in proxy_path:
            return {"public_ips": 1}

        return {}

    @staticmethod
    async def forward_to_region(
        request: Request,
        db: AsyncSession,
        proxy_path: str,
    ) -> Response:
        # 1. Extract and verify JWT
        auth_header = request.headers.get("Authorization", "")
        if not auth_header.startswith("bearer ") and not auth_header.startswith("Bearer "):
            raise HTTPException(status_code=401, detail="Missing or invalid Authorization header")

        token_str = auth_header.split(" ", 1)[1]
        claims = verify_access_token(token_str)
        if claims is None:
            raise HTTPException(status_code=401, detail="Invalid or expired token")

        # 2. Check jti revocation
        jti = claims.get("jti")
        if jti:
            result = await db.execute(
                select(TokenRevocation).where(TokenRevocation.jti == jti)
            )
            if result.scalars().first():
                raise HTTPException(status_code=401, detail="Token has been revoked")

        # 3. Resolve region
        region_name = claims.get("region", "")
        result = await db.execute(
            select(Region).where(Region.name == region_name)
        )
        region_obj = result.scalars().first()
        if not region_obj:
            raise HTTPException(status_code=404, detail=f"Region '{region_name}' not found")
        if not region_obj.is_available:
            raise HTTPException(status_code=503, detail=f"Region '{region_name}' is currently unavailable")

        # 4. Resolve UUIDs to internal integer IDs
        user_uuid = claims.get("sub", "")
        org_uuid = claims.get("org_id", "")

        user_internal_id = ""
        if user_uuid:
            user_result = await db.execute(
                select(User.id).where(User.uuid == user_uuid)
            )
            row = user_result.scalar()
            if row:
                user_internal_id = str(row)

        org_internal_id = 0
        org_obj = None
        if org_uuid:
            org_result = await db.execute(
                select(Organization).where(Organization.uuid == org_uuid)
            )
            org_obj = org_result.scalars().first()
            if org_obj:
                org_internal_id = org_obj.id

        # Build forwarded headers
        forwarded_headers = {
            "X-User-ID": user_internal_id,
            "X-User-Email": claims.get("email", ""),
            "X-Org-ID": str(org_internal_id),
            "X-Org-Name": claims.get("org_name", ""),
            "X-Org-Role": str(claims.get("or", 0)),
            "X-Is-Owner": str(claims.get("is_owner", False)).lower(),
            "X-System-Role": str(claims.get("sr", 0)),
            "X-Forwarded-Secret": region_obj.internal_secret,
        }

        # 5. [NEW] Quota check — superuser and system org skip
        quota_action = None
        reserved = False
        resource_amount = None
        shrink_amount = {}

        is_superuser = claims.get("sr") == 1
        is_system_org = org_obj and org_obj.org_type == OrgType.SYSTEM

        if org_internal_id:
            quota_action = ProxyService._match_quota_rule(request.method, proxy_path)

            if quota_action == "consume":
                # CREATE: extract resource amount, check quota and reserve
                resource_amount = await ProxyService._extract_resource_amount(
                    request, proxy_path, region_obj, forwarded_headers,
                )
                if resource_amount:
                    await quota_service.check_and_reserve(
                        db, org_internal_id, region_obj.id, resource_amount,
                    )
                    reserved = True

            elif quota_action == "release":
                # DELETE: query current resource amount before forwarding
                resource_id = ProxyService._extract_resource_id(proxy_path)
                resource_amount = await quota_service.query_resource_amount(
                    region_obj, proxy_path, resource_id, forwarded_headers,
                )

            elif quota_action == "resize":
                # RESIZE: calculate diff between old and new
                body = await request.json()
                resource_id = ProxyService._extract_resource_id(proxy_path)
                resource_diff = {}

                if "instances" in proxy_path:
                    new_flavor_id = body.get("flavor")
                    if new_flavor_id is not None:
                        old_amount = await quota_service.query_resource_amount(
                            region_obj, proxy_path, resource_id, forwarded_headers,
                        )
                        new_amount = await quota_service.query_flavor_amount(
                            region_obj, new_flavor_id, forwarded_headers,
                        )
                        resource_diff = {
                            k: new_amount.get(k, 0) - old_amount.get(k, 0)
                            for k in old_amount
                        }
                    else:
                        quota_action = None

                elif "volumes" in proxy_path:
                    new_size = body.get("size")
                    if new_size is not None:
                        old_amount = await quota_service.query_resource_amount(
                            region_obj, proxy_path, resource_id, forwarded_headers,
                        )
                        resource_diff = {
                            "disk_gb": float(new_size) - old_amount.get("disk_gb", 0),
                        }
                    else:
                        quota_action = None

                if resource_diff:
                    reserve_amount = {k: v for k, v in resource_diff.items() if v > 0}
                    shrink_amount = {k: abs(v) for k, v in resource_diff.items() if v < 0}
                    if reserve_amount:
                        await quota_service.check_and_reserve(
                            db, org_internal_id, region_obj.id, reserve_amount,
                        )
                        reserved = True
                        resource_amount = reserve_amount

        # 6. Construct backend URL
        resolved_path = proxy_path
        for param_name, param_value in request.path_params.items():
            resolved_path = resolved_path.replace(f"{{{param_name}}}", str(param_value))

        clean_path = resolved_path.lstrip("/")
        backend_url = build_backend_url(region_obj.internal_endpoint, clean_path)

        query_params = dict(request.query_params)
        query_params.pop("region", None)

        logger.info(f"Proxy: {request.method} -> {backend_url} (user={claims.get('sub')}, org={claims.get('org_id')})")

        # 7. Forward request
        try:
            async with httpx.AsyncClient(verify=False) as client:
                client_headers = {}
                for k, v in request.headers.items():
                    if k.lower() not in HOP_BY_HOP:
                        client_headers[k] = v

                client_headers.update(forwarded_headers)
                client_headers.pop("authorization", None)
                client_headers.pop("Authorization", None)

                body = None
                if request.method not in ("GET", "DELETE", "HEAD", "OPTIONS"):
                    body = await request.body()

                proxy_response = await client.request(
                    method=request.method,
                    url=backend_url,
                    params=query_params,
                    headers=client_headers,
                    content=body,
                    timeout=settings.PROXY_TIMEOUT_SECONDS,
                )

                if proxy_response.status_code >= 400:
                    logger.warning(f"Backend {proxy_response.status_code}: {backend_url}")

                # 8. Post-quota handling
                if proxy_response.status_code in (200, 201, 204):
                    # Success
                    if quota_action == "release" and resource_amount:
                        await quota_service.release(db, org_internal_id, region_obj.id, resource_amount)
                    elif quota_action == "resize" and shrink_amount:
                        await quota_service.release(db, org_internal_id, region_obj.id, shrink_amount)
                else:
                    # Failure — rollback reservation
                    if reserved:
                        await quota_service.release(db, org_internal_id, region_obj.id, resource_amount)

                response_headers = {}
                for k, v in proxy_response.headers.items():
                    if k.lower() not in HOP_BY_HOP:
                        response_headers[k] = v

                return Response(
                    content=proxy_response.content,
                    status_code=proxy_response.status_code,
                    headers=response_headers,
                )
        except httpx.RequestError as e:
            # Network error — rollback reservation
            if reserved:
                await quota_service.release(db, org_internal_id, region_obj.id, resource_amount)
            logger.error(f"Backend request failed: {backend_url}: {e}")
            raise HTTPException(status_code=502, detail=f"Backend request failed: {str(e)}")


proxy_service = ProxyService()

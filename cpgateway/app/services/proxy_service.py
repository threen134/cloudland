import httpx
from datetime import datetime, timezone
from fastapi import Request, Response, HTTPException
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.region import Region
from app.models.user import User
from app.models.org import Organization
from app.models.token_revocation import TokenRevocation
from app.core.security import verify_access_token
from app.core.config import settings
from app.core.logging_config import logger


HOP_BY_HOP = frozenset([
    'connection', 'keep-alive', 'proxy-authenticate',
    'proxy-authorization', 'te', 'trailers',
    'transfer-encoding', 'upgrade', 'host',
    'content-length', 'authorization'
])


class ProxyService:
    """
    统一 API Gateway / Reverse Proxy。
    所有前端对 Cloudland 资源的请求统一经 CPGateway 转发。

    流程:
    1. 验签 JWT (RS256)
    2. 检查 jti 是否已被吊销
    3. 从 claims.region 查 regions 表，获取内网地址
    4. 转发请求到 Cloudland 内网，携带 X-* Header
    5. 透传 Cloudland 响应
    """

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

        # 4. Resolve UUIDs to internal integer IDs for Cloudland backend
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

        org_internal_id = ""
        if org_uuid:
            org_result = await db.execute(
                select(Organization.id).where(Organization.uuid == org_uuid)
            )
            row = org_result.scalar()
            if row:
                org_internal_id = str(row)

        # Build forwarded headers (Cloudland backend expects integer IDs)
        forwarded_headers = {
            "X-User-ID": user_internal_id,
            "X-User-Email": claims.get("email", ""),
            "X-Org-ID": org_internal_id,
            "X-Org-Name": claims.get("org_name", ""),
            "X-Org-Role": str(claims.get("or", 0)),
            "X-Is-Owner": str(claims.get("is_owner", False)).lower(),
            "X-System-Role": str(claims.get("sr", 0)),
            "X-Forwarded-Secret": region_obj.internal_secret,
        }

        # 5. Construct backend URL
        resolved_path = proxy_path
        for param_name, param_value in request.path_params.items():
            resolved_path = resolved_path.replace(f"{{{param_name}}}", str(param_value))

        clean_path = resolved_path.lstrip("/")
        base_url = region_obj.internal_endpoint.strip().rstrip("/")
        if not base_url.endswith(settings.BACKEND_API_SUFFIX):
            base_url = f"{base_url}{settings.BACKEND_API_SUFFIX}"
        backend_url = f"{base_url}/{clean_path}"

        query_params = dict(request.query_params)
        query_params.pop("region", None)

        logger.info(f"Proxy: {request.method} -> {backend_url} (user={claims.get('sub')}, org={claims.get('org_id')})")

        # 6. Forward request
        try:
            async with httpx.AsyncClient(verify=False) as client:
                # Filter headers
                client_headers = {}
                for k, v in request.headers.items():
                    if k.lower() not in HOP_BY_HOP:
                        client_headers[k] = v

                # Replace with internal headers
                client_headers.update(forwarded_headers)
                # Remove original Authorization
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
            logger.error(f"Backend request failed: {backend_url}: {e}")
            raise HTTPException(status_code=502, detail=f"Backend request failed: {str(e)}")


proxy_service = ProxyService()

from fastapi import FastAPI, Request
from app.core.config import settings
from app.core.logging_config import setup_logging, logger
from app.api.endpoints import auth, users, resources, regions, orgs
from app.api.endpoints.cloudland import compute, network, authorization, zone, administration, alarm
from app.core.database import engine, Base, AsyncSessionLocal
import time

setup_logging()

app = FastAPI(
    title=settings.PROJECT_NAME,
    description="统一用户与 Org 管理平台，支持 RS256 JWT、Org/Region 切换及 API Gateway 转发。",
    version="2.0.0",
    openapi_url=f"{settings.API_V1_STR}/openapi.json",
)


@app.middleware("http")
async def log_requests(request: Request, call_next):
    start_time = time.time()
    response = await call_next(request)
    duration = time.time() - start_time
    logger.info(
        f"Method: {request.method} Path: {request.url.path} Status: {response.status_code} Duration: {duration:.3f}s"
    )
    return response


@app.on_event("startup")
async def startup():
    """
    启动事件:
    1. 建表（含新增的 organizations / members / token_revocations 表）
    2. 创建 Root 超级管理员用户
    """
    from app.models.user import User, SystemRole, UserStatus
    from app.models.org import Organization
    from app.models.member import Member
    from app.models.token_revocation import TokenRevocation
    from app.core.security import get_password_hash
    from sqlalchemy.future import select

    # Create all tables
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    # Initialize root user
    async with AsyncSessionLocal() as db:
        result = await db.execute(
            select(User).where(User.username == settings.FIRST_SUPERUSER_USERNAME)
        )
        root_user = result.scalars().first()

        if not root_user:
            logger.info(f"Creating root superuser: {settings.FIRST_SUPERUSER_USERNAME}")
            import uuid
            root_user = User(
                uuid=str(uuid.uuid4()),
                email=settings.FIRST_SUPERUSER_EMAIL,
                username=settings.FIRST_SUPERUSER_USERNAME,
                hashed_password=get_password_hash(settings.FIRST_SUPERUSER_PASSWORD),
                language="en",
                is_active=True,
                is_superuser=True,
                system_role=SystemRole.ADMIN,
                status=UserStatus.ACTIVE,
            )
            db.add(root_user)
            await db.commit()
            logger.info("Root superuser created successfully")
        else:
            if root_user.system_role != SystemRole.ADMIN:
                root_user.system_role = SystemRole.ADMIN
                root_user.status = UserStatus.ACTIVE
                await db.commit()
            logger.info("Root superuser already exists, skipping creation")

        # Ensure default "admin" organization exists and admin user is a member
        await db.refresh(root_user)
        from app.models.member import OrgRole
        admin_org = (await db.execute(
            select(Organization).where(Organization.slug == "admin", Organization.deleted_at.is_(None))
        )).scalars().first()

        if not admin_org:
            import uuid as _uuid
            admin_org = Organization(
                uuid=str(_uuid.uuid4()),
                name="Admin",
                slug="admin",
                org_type=2,  # SYSTEM type
                owner_user_id=root_user.id,
            )
            db.add(admin_org)
            await db.commit()
            await db.refresh(admin_org)
            logger.info(f"Default 'Admin' organization created (id={admin_org.id})")

        # Ensure admin user is a member of the admin org
        admin_member = (await db.execute(
            select(Member).where(
                Member.user_id == root_user.id,
                Member.org_id == admin_org.id,
                Member.deleted_at.is_(None),
            )
        )).scalars().first()

        if not admin_member:
            import uuid as _uuid
            admin_member = Member(
                uuid=str(_uuid.uuid4()),
                user_id=root_user.id,
                org_id=admin_org.id,
                org_role=OrgRole.ADMIN,
            )
            db.add(admin_member)
            await db.commit()
            logger.info("Admin user added to 'Admin' organization as ADMIN")

    logger.info("Application startup complete. All tables created/verified. Routes registered.")


# --- Routes ---

# Auth & User Management (CPGateway handles)
app.include_router(auth.router, prefix=f"{settings.API_V1_STR}/auth", tags=["auth"])
app.include_router(users.router, prefix=f"{settings.API_V1_STR}/users", tags=["users"])
app.include_router(orgs.router, prefix=f"{settings.API_V1_STR}/orgs", tags=["orgs"])
app.include_router(regions.router, prefix=f"{settings.API_V1_STR}/regions", tags=["regions"])
app.include_router(resources.router, prefix=f"{settings.API_V1_STR}/resources", tags=["resources"])


# Cloudland Proxy Routes (forwarded to Cloudland internal)
app.include_router(compute.router, prefix=f"{settings.API_V1_STR}", tags=["Compute"])
app.include_router(network.router, prefix=f"{settings.API_V1_STR}", tags=["Network"])
app.include_router(authorization.router, prefix=f"{settings.API_V1_STR}", tags=["Authorization"])
app.include_router(zone.router, prefix=f"{settings.API_V1_STR}", tags=["Zone"])
app.include_router(administration.router, prefix=f"{settings.API_V1_STR}", tags=["Administration"])
app.include_router(alarm.router, prefix=f"{settings.API_V1_STR}", tags=["Alarm"])


@app.get("/")
async def root():
    return {"message": "Welcome to Cloudland Control Plane Gateway v2.0"}


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(
        "app.main:app",
        host=settings.LISTEN_ADDR,
        port=settings.LISTEN_PORT,
        reload=settings.DEBUG,
        reload_dirs=["app"],
    )

from fastapi import APIRouter, Depends, HTTPException, status, Request
from fastapi.responses import JSONResponse
from fastapi.security import OAuth2PasswordRequestForm
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from typing import List

from app.schemas.user import UserCreate, UserRegisterResponse, Msg
from app.schemas.token import TokenWithContext, LoginRequest, SwitchOrgRequest, SwitchRegionRequest
from app.schemas.org import UserOrgItem
from app.core.database import get_db
from app.core.security import verify_access_token
from app.services.auth_service import auth_service
from app.core.logging_config import logger
from app.models.user import User
from app.models.org import Organization
from app.models.member import Member

router = APIRouter()


def _extract_claims(request: Request) -> dict:
    """从 Authorization Header 提取并验证 JWT claims。"""
    auth_header = request.headers.get("Authorization", "")
    if not auth_header.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="Missing or invalid Authorization header")
    token_str = auth_header.split(" ", 1)[1]
    claims = verify_access_token(token_str)
    if claims is None:
        raise HTTPException(status_code=401, detail="Invalid or expired token")
    return claims


# --- Registration & Activation ---

@router.post("/register", response_model=UserRegisterResponse, status_code=status.HTTP_201_CREATED)
async def register(user_in: UserCreate, db: AsyncSession = Depends(get_db)):
    """用户注册"""
    try:
        user = await auth_service.register_user(db, user_in)
        return {"message": "Verification email sent", "user": user}
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    except Exception as e:
        from sqlalchemy.exc import IntegrityError
        if isinstance(e, IntegrityError):
            raise HTTPException(status_code=400, detail="Email or username already exists")
        logger.error(f"Registration error: {e}")
        raise HTTPException(status_code=500, detail="Internal server error during registration")


@router.get("/activate", response_model=Msg)
async def activate_account(token: str, db: AsyncSession = Depends(get_db)):
    try:
        result = await auth_service.activate_user(db, token)
        return result
    except ValueError as e:
        return JSONResponse(
            status_code=status.HTTP_400_BAD_REQUEST,
            content={"detail": str(e), "result": False},
        )
    except Exception as e:
        logger.error(f"Activation error: {e}")
        return JSONResponse(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            content={"detail": "Internal server error during activation", "result": False},
        )


# --- Login & Token ---

@router.post("/token", response_model=TokenWithContext)
async def login(login_in: LoginRequest, db: AsyncSession = Depends(get_db)):
    """登录并签发 RS256 JWT（含 org_id / region / roles）"""
    try:
        result = await auth_service.login_user(
            db, login_in.username, login_in.password,
            org_id=login_in.org_id, region=login_in.region,
        )
        return result
    except ValueError as e:
        raise HTTPException(status_code=401, detail=str(e), headers={"WWW-Authenticate": "Bearer"})
    except PermissionError as e:
        raise HTTPException(status_code=403, detail=str(e))
    except Exception as e:
        logger.error(f"Login error: {e}")
        raise HTTPException(status_code=500, detail="Internal server error during login")


@router.post("/token/form", response_model=TokenWithContext)
async def login_form(form_data: OAuth2PasswordRequestForm = Depends(), db: AsyncSession = Depends(get_db)):
    """OAuth2 表单登录（兼容旧接口）"""
    try:
        result = await auth_service.login_user(db, form_data.username, form_data.password)
        return result
    except ValueError as e:
        raise HTTPException(status_code=401, detail=str(e), headers={"WWW-Authenticate": "Bearer"})
    except PermissionError as e:
        raise HTTPException(status_code=403, detail=str(e))
    except Exception as e:
        logger.error(f"Login error: {e}")
        raise HTTPException(status_code=500, detail="Internal server error during login")


# --- Switch Org / Region ---

@router.post("/switch-org", response_model=TokenWithContext)
async def switch_org(
    switch_in: SwitchOrgRequest,
    request: Request,
    db: AsyncSession = Depends(get_db),
):
    """切换 Org（可选同时切换 Region），签发新 Token，吊销旧 Token。"""
    claims = _extract_claims(request)
    try:
        result = await auth_service.switch_org(
            db, claims, switch_in.org_id, region=switch_in.region,
        )
        return result
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except PermissionError as e:
        raise HTTPException(status_code=403, detail=str(e))


@router.post("/switch-region", response_model=TokenWithContext)
async def switch_region(
    switch_in: SwitchRegionRequest,
    request: Request,
    db: AsyncSession = Depends(get_db),
):
    """切换 Region（Org 不变），签发新 Token，吊销旧 Token。"""
    claims = _extract_claims(request)
    try:
        result = await auth_service.switch_region(db, claims, switch_in.region)
        return result
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))


# --- Token Revocation ---

@router.post("/token/revoke", status_code=status.HTTP_204_NO_CONTENT)
async def revoke_token(
    request: Request,
    db: AsyncSession = Depends(get_db),
):
    """吊销当前 Token。"""
    claims = _extract_claims(request)
    await auth_service.revoke_token(db, claims)


# --- User Context ---

@router.get("/me")
async def get_me(request: Request, db: AsyncSession = Depends(get_db)):
    """获取当前用户信息"""
    claims = _extract_claims(request)
    user_id = int(claims["sub"])
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return {
        "id": user.id,
        "email": user.email,
        "username": user.username,
        "first_name": user.first_name,
        "last_name": user.last_name,
        "system_role": user.system_role,
        "status": user.status,
        "current_org_id": claims.get("org_id"),
        "current_region": claims.get("region"),
    }


@router.get("/me/orgs", response_model=List[UserOrgItem])
async def get_my_orgs(request: Request, db: AsyncSession = Depends(get_db)):
    """查询当前用户所属的所有 Org（供前端切换 Org 下拉框使用）"""
    claims = _extract_claims(request)
    user_id = int(claims["sub"])
    current_org_id = claims.get("org_id", "0")

    result = await db.execute(
        select(Member, Organization)
        .join(Organization, Organization.id == Member.org_id)
        .where(
            Member.user_id == user_id,
            Member.deleted_at.is_(None),
            Organization.deleted_at.is_(None),
        )
    )
    rows = result.all()
    return [
        UserOrgItem(
            org_id=org.id,
            name=org.name,
            slug=org.slug,
            org_role=member.org_role,
            is_owner=(org.owner_user_id == user_id),
            is_current=(str(org.id) == str(current_org_id)),
        )
        for member, org in rows
    ]


# --- Public Key ---

@router.get("/public-key")
async def get_public_key():
    """返回 RS256 公钥 PEM，供外部服务验签 JWT。无需鉴权。"""
    from app.core.security import _get_public_key
    pem = _get_public_key()
    return {"public_key": pem}

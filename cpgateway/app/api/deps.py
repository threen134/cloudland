from fastapi import Depends, HTTPException, status
from fastapi.security import OAuth2PasswordBearer
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from app.core.security import verify_access_token
from app.core.config import settings
from app.core.database import get_db
from app.models.user import User, SystemRole

oauth2_scheme = OAuth2PasswordBearer(
    tokenUrl=f"{settings.API_V1_STR}/auth/token/form"
)


async def get_current_user(
    token: str = Depends(oauth2_scheme),
    db: AsyncSession = Depends(get_db),
) -> User:
    """从 RS256 Bearer Token 中解析用户。"""
    claims = verify_access_token(token)
    if claims is None:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Could not validate credentials",
        )

    user_uuid = claims.get("sub")
    if user_uuid is None:
        raise HTTPException(status_code=403, detail="Invalid token: missing subject")

    result = await db.execute(
        select(User).where(User.uuid == user_uuid)
    )
    user = result.scalars().first()

    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return user


async def get_current_active_user(
    current_user: User = Depends(get_current_user),
) -> User:
    """确保用户已激活。"""
    if not current_user.is_active:
        raise HTTPException(status_code=400, detail="Inactive user")
    return current_user


async def get_current_superuser(
    current_user: User = Depends(get_current_active_user),
) -> User:
    """确保用户是系统管理员。"""
    if not (current_user.is_superuser or current_user.system_role == SystemRole.ADMIN):
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="The user doesn't have enough privileges",
        )
    return current_user

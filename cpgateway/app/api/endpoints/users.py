from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from typing import List, Optional
from datetime import datetime, timezone

from app.core.database import get_db
from app.core.security import get_password_hash, verify_password
from app.api.deps import get_current_active_user, get_current_superuser
from app.models.user import User, SystemRole, UserStatus
from app.models.member import Member
from app.models.org import Organization
from app.schemas.user import User as UserSchema, UserProfileUpdate, PasswordChange

router = APIRouter()


@router.get("", response_model=List[UserSchema])
async def list_users(
    is_active: Optional[bool] = None,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """列出所有用户 (仅 SystemAdmin)。"""
    query = select(User)
    if is_active is not None:
        query = query.where(User.is_active == is_active)
    result = await db.execute(query)
    return result.scalars().all()


@router.get("/{user_uuid}", response_model=UserSchema)
async def get_user(
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """获取特定用户详情 (仅 SystemAdmin)。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail=f"User with UUID {user_uuid} not found")
    return user


@router.put("/{user_uuid}/enable")
async def enable_user(
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """启用用户 (仅 SystemAdmin)。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Determine correct status: Active if has memberships, Dormant if none
    member_result = await db.execute(
        select(Member).where(Member.user_id == user.id, Member.deleted_at.is_(None)).limit(1)
    )
    has_membership = member_result.scalars().first() is not None
    user.status = UserStatus.ACTIVE if has_membership else UserStatus.DORMANT
    user.is_active = True
    await db.commit()
    return {"status": "ok", "new_status": user.status}


@router.put("/{user_uuid}/disable")
async def disable_user(
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """禁用用户 (仅 SystemAdmin)。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    if user.id == current_user.id:
        raise HTTPException(status_code=400, detail="Cannot disable yourself")
    user.status = UserStatus.DISABLED
    await db.commit()
    return {"status": "ok"}


@router.put("/{user_uuid}/demote")
async def demote_user(
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """降级 SystemAdmin → SystemUser (仅 SystemAdmin)。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    if user.id == current_user.id:
        raise HTTPException(status_code=400, detail="Cannot demote yourself")
    if user.system_role != SystemRole.ADMIN:
        raise HTTPException(status_code=400, detail="User is not a SystemAdmin")

    # Ensure at least one SystemAdmin remains
    count_result = await db.execute(
        select(User).where(User.system_role == SystemRole.ADMIN)
    )
    admin_count = len(count_result.scalars().all())
    if admin_count <= 1:
        raise HTTPException(status_code=400, detail="Cannot demote the last SystemAdmin")

    user.system_role = SystemRole.USER
    user.is_superuser = False
    await db.commit()
    return {"status": "ok"}


@router.delete("/{user_uuid}")
async def delete_user(
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """删除用户 (仅 SystemAdmin)。需先处理 Org 所有权。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    if user.id == current_user.id:
        raise HTTPException(status_code=400, detail="Cannot delete yourself")

    # Check if user is Owner of any Org with other members
    org_result = await db.execute(
        select(Organization).where(
            Organization.owner_user_id == user.id,
            Organization.deleted_at.is_(None),
        )
    )
    for org in org_result.scalars().all():
        member_count_result = await db.execute(
            select(Member).where(
                Member.org_id == org.id,
                Member.deleted_at.is_(None),
            )
        )
        if len(member_count_result.scalars().all()) > 1:
            raise HTTPException(
                status_code=400,
                detail=f"User is owner of Org '{org.name}' which has other members. Transfer ownership first.",
            )

    now = datetime.now(timezone.utc)

    # Soft-delete all memberships
    member_result = await db.execute(
        select(Member).where(Member.user_id == user.id, Member.deleted_at.is_(None))
    )
    for member in member_result.scalars().all():
        member.deleted_at = now

    # Soft-delete user — mangle email/username to free up uniqueness constraints
    ts = int(now.timestamp())
    user.is_active = False
    user.status = UserStatus.DISABLED
    user.email = f"del{ts}+{user.email}"
    user.username = f"{user.username}_del{ts}"
    await db.commit()
    return {"status": "ok"}


@router.patch("/{user_uuid}/profile")
async def update_profile(
    user_uuid: str,
    profile_in: UserProfileUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """修改用户 Profile。普通用户只能修改自己，SystemAdmin 可修改任何人。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Permission check
    if user.id != current_user.id and current_user.system_role != SystemRole.ADMIN:
        raise HTTPException(status_code=403, detail="Can only update own profile")

    # Remark can only be set by SystemAdmin
    if profile_in.remark is not None and current_user.system_role != SystemRole.ADMIN:
        raise HTTPException(status_code=403, detail="Only SystemAdmin can modify remark")

    if profile_in.first_name is not None:
        user.first_name = profile_in.first_name
    if profile_in.last_name is not None:
        user.last_name = profile_in.last_name
    if profile_in.language is not None:
        user.language = profile_in.language
    if profile_in.remark is not None:
        user.remark = profile_in.remark

    await db.commit()
    return {"status": "ok"}


@router.put("/{user_uuid}/password")
async def change_password(
    user_uuid: str,
    password_in: PasswordChange,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """修改密码。普通用户需验证旧密码，SystemAdmin 可重置任何人密码。"""
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Permission check
    if user.id != current_user.id and current_user.system_role != SystemRole.ADMIN:
        raise HTTPException(status_code=403, detail="Can only change own password")

    # Verify old password (skip if SystemAdmin resetting someone else's)
    if not (current_user.system_role == SystemRole.ADMIN and current_user.id != user.id):
        if not verify_password(password_in.old_password, user.hashed_password):
            raise HTTPException(status_code=400, detail="Incorrect old password")

    user.hashed_password = get_password_hash(password_in.new_password)
    await db.commit()
    return {"status": "ok"}

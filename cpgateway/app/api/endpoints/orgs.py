from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from sqlalchemy import func as sa_func
from typing import List

from app.core.database import get_db
from app.api.deps import get_current_active_user, get_current_superuser
from app.models.user import User, SystemRole
from app.models.org import Organization, OrgType
from app.models.member import Member, OrgRole
from app.schemas.org import (
    OrgCreate, OrgUpdate, OrgResponse, OrgDetail,
    MemberAdd, MemberUpdate, MemberResponse, TransferOwner,
)

router = APIRouter()


# --- Org CRUD ---

@router.post("", response_model=OrgResponse, status_code=status.HTTP_201_CREATED)
async def create_org(
    org_in: OrgCreate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """创建 Org（仅 SystemAdmin）"""
    # Check slug uniqueness
    result = await db.execute(
        select(Organization).where(
            Organization.slug == org_in.slug,
            Organization.deleted_at.is_(None),
        )
    )
    if result.scalars().first():
        raise HTTPException(status_code=400, detail=f"Slug '{org_in.slug}' already exists")

    org = Organization(
        name=org_in.name,
        slug=org_in.slug,
        org_type=OrgType.TEAM,
        owner_user_id=current_user.id,
    )
    db.add(org)
    await db.commit()
    await db.refresh(org)

    # Add owner as Admin member
    member = Member(
        user_id=current_user.id,
        org_id=org.id,
        org_role=OrgRole.ADMIN,
    )
    db.add(member)
    await db.commit()

    return org


@router.get("", response_model=List[OrgResponse])
async def list_orgs(
    skip: int = 0,
    limit: int = 100,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """列出 Org（SystemAdmin 全量，普通用户仅自己的）"""
    if current_user.system_role == SystemRole.ADMIN or current_user.is_superuser:
        result = await db.execute(
            select(Organization)
            .where(Organization.deleted_at.is_(None))
            .offset(skip).limit(limit)
        )
    else:
        result = await db.execute(
            select(Organization)
            .join(Member, Member.org_id == Organization.id)
            .where(
                Member.user_id == current_user.id,
                Member.deleted_at.is_(None),
                Organization.deleted_at.is_(None),
            )
            .offset(skip).limit(limit)
        )
    return result.scalars().all()


@router.get("/{org_id}", response_model=OrgDetail)
async def get_org(
    org_id: int,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """Org 详情"""
    result = await db.execute(
        select(Organization).where(
            Organization.id == org_id,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    # Check access
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        member_result = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org_id,
                Member.deleted_at.is_(None),
            )
        )
        if not member_result.scalars().first():
            raise HTTPException(status_code=403, detail="Not a member of this organization")

    # Count members
    count_result = await db.execute(
        select(sa_func.count(Member.id)).where(
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    member_count = count_result.scalar() or 0

    # Get owner email
    owner_result = await db.execute(select(User).where(User.id == org.owner_user_id))
    owner = owner_result.scalars().first()

    return OrgDetail(
        id=org.id,
        uuid=org.uuid,
        name=org.name,
        slug=org.slug,
        org_type=org.org_type,
        owner_user_id=org.owner_user_id,
        created_at=org.created_at,
        member_count=member_count,
        owner_email=owner.email if owner else None,
    )


@router.patch("/{org_id}", response_model=OrgResponse)
async def update_org(
    org_id: int,
    org_in: OrgUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """改名（OrgOwner / OrgAdmin / SystemAdmin）"""
    result = await db.execute(
        select(Organization).where(
            Organization.id == org_id,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    # Permission: SystemAdmin, OrgOwner, or OrgAdmin
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        if org.owner_user_id != current_user.id:
            member_result = await db.execute(
                select(Member).where(
                    Member.user_id == current_user.id,
                    Member.org_id == org_id,
                    Member.org_role >= OrgRole.ADMIN,
                    Member.deleted_at.is_(None),
                )
            )
            if not member_result.scalars().first():
                raise HTTPException(status_code=403, detail="Not enough permissions")

    if org_in.name is not None:
        org.name = org_in.name

    await db.commit()
    await db.refresh(org)
    return org


@router.delete("/{org_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_org(
    org_id: int,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """解散 Org（仅 SystemAdmin，软删除）"""
    result = await db.execute(
        select(Organization).where(
            Organization.id == org_id,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    if org.org_type == OrgType.SYSTEM:
        raise HTTPException(status_code=400, detail="Cannot delete system organization")

    from datetime import datetime, timezone
    org.deleted_at = datetime.now(timezone.utc)
    await db.commit()


# --- Member Management ---

@router.get("/{org_id}/members", response_model=List[MemberResponse])
async def list_members(
    org_id: int,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """列出 Org 成员"""
    # Verify org exists
    org_result = await db.execute(
        select(Organization).where(
            Organization.id == org_id,
            Organization.deleted_at.is_(None),
        )
    )
    if not org_result.scalars().first():
        raise HTTPException(status_code=404, detail="Organization not found")

    result = await db.execute(
        select(Member, User.email)
        .join(User, User.id == Member.user_id)
        .where(
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    rows = result.all()
    return [
        MemberResponse(
            id=m.id, uuid=m.uuid, user_id=m.user_id, org_id=m.org_id,
            org_role=m.org_role, user_email=email, created_at=m.created_at,
        )
        for m, email in rows
    ]


@router.post("/{org_id}/members", response_model=MemberResponse, status_code=status.HTTP_201_CREATED)
async def add_member(
    org_id: int,
    member_in: MemberAdd,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """添加成员"""
    # Permission: SystemAdmin or OrgAdmin+
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        member_result = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org_id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not member_result.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions to add members")

    # Check target user exists
    user_result = await db.execute(select(User).where(User.id == member_in.user_id))
    target_user = user_result.scalars().first()
    if not target_user:
        raise HTTPException(status_code=404, detail="User not found")

    # Check not already a member
    existing = await db.execute(
        select(Member).where(
            Member.user_id == member_in.user_id,
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    if existing.scalars().first():
        raise HTTPException(status_code=400, detail="User is already a member of this organization")

    member = Member(
        user_id=member_in.user_id,
        org_id=org_id,
        org_role=member_in.org_role,
    )
    db.add(member)
    await db.commit()
    await db.refresh(member)

    return MemberResponse(
        id=member.id, uuid=member.uuid, user_id=member.user_id, org_id=member.org_id,
        org_role=member.org_role, user_email=target_user.email, created_at=member.created_at,
    )


@router.patch("/{org_id}/members/{user_id}", response_model=MemberResponse)
async def update_member_role(
    org_id: int,
    user_id: int,
    member_in: MemberUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """修改成员角色"""
    # Permission check
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        my_member = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org_id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not my_member.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions")

    result = await db.execute(
        select(Member).where(
            Member.user_id == user_id,
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    member = result.scalars().first()
    if not member:
        raise HTTPException(status_code=404, detail="Member not found")

    member.org_role = member_in.org_role
    await db.commit()
    await db.refresh(member)

    user_result = await db.execute(select(User).where(User.id == user_id))
    target_user = user_result.scalars().first()

    return MemberResponse(
        id=member.id, uuid=member.uuid, user_id=member.user_id, org_id=member.org_id,
        org_role=member.org_role, user_email=target_user.email if target_user else None,
        created_at=member.created_at,
    )


@router.delete("/{org_id}/members/{user_id}", status_code=status.HTTP_204_NO_CONTENT)
async def remove_member(
    org_id: int,
    user_id: int,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """移除成员"""
    # Permission check
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        my_member = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org_id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not my_member.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions")

    # Cannot remove the Org Owner
    org_result = await db.execute(
        select(Organization).where(Organization.id == org_id)
    )
    org = org_result.scalars().first()
    if org and org.owner_user_id == user_id:
        raise HTTPException(status_code=400, detail="Cannot remove the organization owner. Transfer ownership first.")

    result = await db.execute(
        select(Member).where(
            Member.user_id == user_id,
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    member = result.scalars().first()
    if not member:
        raise HTTPException(status_code=404, detail="Member not found")

    from datetime import datetime, timezone
    member.deleted_at = datetime.now(timezone.utc)
    await db.commit()


@router.post("/{org_id}/transfer-owner", response_model=OrgResponse)
async def transfer_owner(
    org_id: int,
    transfer_in: TransferOwner,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """转让 Owner（仅 SystemAdmin）"""
    result = await db.execute(
        select(Organization).where(
            Organization.id == org_id,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")

    # Verify new owner is a member
    member_result = await db.execute(
        select(Member).where(
            Member.user_id == transfer_in.new_owner_user_id,
            Member.org_id == org_id,
            Member.deleted_at.is_(None),
        )
    )
    if not member_result.scalars().first():
        raise HTTPException(status_code=400, detail="New owner must be a member of the organization")

    org.owner_user_id = transfer_in.new_owner_user_id
    await db.commit()
    await db.refresh(org)
    return org

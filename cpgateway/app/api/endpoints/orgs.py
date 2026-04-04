from fastapi import APIRouter, BackgroundTasks, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select
from sqlalchemy import func as sa_func
from typing import List

from app.core.database import get_db
from app.api.deps import get_current_active_user, get_current_superuser
from app.models.user import User, SystemRole
from app.models.org import Organization, OrgType, OrgStatus
from app.models.member import Member, OrgRole, InvitationStatus
from app.schemas.org import (
    OrgCreate, OrgUpdate, OrgResponse, OrgDetail,
    MemberAdd, MemberUpdate, MemberResponse, TransferOwner, OrgStatusUpdate,
)
from app.schemas.invitation import InvitationCreate, InvitationResponse
from app.services.invitation_service import invitation_service
from app.services.quota_service import initialize_org_quotas
from app.services.org_sync_service import org_sync_service

router = APIRouter()


# --- Helper to resolve org by uuid ---

async def _get_org_or_404(db: AsyncSession, org_uuid: str) -> Organization:
    result = await db.execute(
        select(Organization).where(
            Organization.uuid == org_uuid,
            Organization.deleted_at.is_(None),
        )
    )
    org = result.scalars().first()
    if not org:
        raise HTTPException(status_code=404, detail="Organization not found")
    return org


async def _get_user_by_uuid_or_404(db: AsyncSession, user_uuid: str) -> User:
    result = await db.execute(select(User).where(User.uuid == user_uuid))
    user = result.scalars().first()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return user


def _build_org_response(org: Organization, owner: User) -> OrgResponse:
    return OrgResponse(
        uuid=org.uuid,
        name=org.name,
        slug=org.slug,
        org_type=org.org_type,
        status=org.status,
        owner_uuid=owner.uuid if owner else "",
        owner_name=owner.username if owner else None,
        owner_email=owner.email if owner else None,
        created_at=org.created_at,
    )


async def _build_member_response(db: AsyncSession, m: Member) -> MemberResponse:
    user_result = await db.execute(select(User).where(User.id == m.user_id))
    user = user_result.scalars().first()
    org_result = await db.execute(select(Organization).where(Organization.id == m.org_id))
    org = org_result.scalars().first()
    return MemberResponse(
        uuid=m.uuid,
        user_uuid=user.uuid if user else "",
        org_uuid=org.uuid if org else "",
        org_role=m.org_role,
        user_email=user.email if user else None,
        created_at=m.created_at,
    )


# --- Org CRUD ---

@router.post("", response_model=OrgResponse, status_code=status.HTTP_201_CREATED)
async def create_org(
    org_in: OrgCreate,
    background_tasks: BackgroundTasks,
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
        status=OrgStatus.ACTIVE,
    )
    db.add(org)
    await db.flush()  # Get org.id without committing

    # Initialize org-region quota/consumption for all available regions
    await initialize_org_quotas(db, org.id)

    # Add owner as Admin member
    member = Member(
        user_id=current_user.id,
        org_id=org.id,
        org_role=OrgRole.ADMIN,
    )
    db.add(member)
    await db.commit()
    await db.refresh(org)

    # 异步同步到所有 Region 的 clapi（不阻塞响应）
    background_tasks.add_task(org_sync_service.sync_org_to_all_regions, db, org)

    return _build_org_response(org, current_user)


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
    orgs = result.scalars().all()

    # Build responses with owner uuid
    responses = []
    for org in orgs:
        owner_result = await db.execute(select(User).where(User.id == org.owner_user_id))
        owner = owner_result.scalars().first()
        responses.append(_build_org_response(org, owner))
    return responses


@router.get("/{org_uuid}", response_model=OrgDetail)
async def get_org(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """Org 详情"""
    org = await _get_org_or_404(db, org_uuid)

    # Check access
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        member_result = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org.id,
                Member.deleted_at.is_(None),
            )
        )
        if not member_result.scalars().first():
            raise HTTPException(status_code=403, detail="Not a member of this organization")

    # Count members
    count_result = await db.execute(
        select(sa_func.count(Member.id)).where(
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    member_count = count_result.scalar() or 0

    # Get owner
    owner_result = await db.execute(select(User).where(User.id == org.owner_user_id))
    owner = owner_result.scalars().first()

    return OrgDetail(
        uuid=org.uuid,
        name=org.name,
        slug=org.slug,
        org_type=org.org_type,
        owner_uuid=owner.uuid if owner else "",
        created_at=org.created_at,
        member_count=member_count,
        owner_email=owner.email if owner else None,
    )


@router.patch("/{org_uuid}", response_model=OrgResponse)
async def update_org(
    org_uuid: str,
    org_in: OrgUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """改名（OrgOwner / OrgAdmin / SystemAdmin）"""
    org = await _get_org_or_404(db, org_uuid)

    # Permission: SystemAdmin, OrgOwner, or OrgAdmin
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        if org.owner_user_id != current_user.id:
            member_result = await db.execute(
                select(Member).where(
                    Member.user_id == current_user.id,
                    Member.org_id == org.id,
                    Member.org_role >= OrgRole.ADMIN,
                    Member.deleted_at.is_(None),
                )
            )
            if not member_result.scalars().first():
                raise HTTPException(status_code=403, detail="Not enough permissions")

    if org_in.name is not None:
        org.name = org_in.name
    if org_in.description is not None:
        org.description = org_in.description

    await db.commit()
    await db.refresh(org)

    owner_result = await db.execute(select(User).where(User.id == org.owner_user_id))
    owner = owner_result.scalars().first()
    return _build_org_response(org, owner)


@router.delete("/{org_uuid}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_org(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """解散 Org（仅 SystemAdmin，软删除）"""
    org = await _get_org_or_404(db, org_uuid)

    if org.org_type == OrgType.SYSTEM:
        raise HTTPException(status_code=400, detail="Cannot delete system organization")

    from datetime import datetime, timezone
    now = datetime.now(timezone.utc)
    ts = int(now.timestamp())
    org.deleted_at = now
    org.slug = f"{org.slug}_del{ts}"
    await db.commit()


@router.patch("/{org_uuid}/status", response_model=OrgResponse)
async def update_org_status(
    org_uuid: str,
    status_in: OrgStatusUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """修改 Org 状态（仅 SystemAdmin）。不允许手动设为 PENDING(0)。"""
    if status_in.status == OrgStatus.PENDING:
        raise HTTPException(status_code=400, detail="Cannot manually set org status to PENDING")
    try:
        new_status = OrgStatus(status_in.status)
    except ValueError:
        raise HTTPException(status_code=400, detail=f"Invalid status value: {status_in.status}")

    org = await _get_org_or_404(db, org_uuid)

    if org.org_type == OrgType.SYSTEM:
        raise HTTPException(status_code=400, detail="Cannot change status of system organization")

    org.status = new_status
    await db.commit()
    await db.refresh(org)

    owner_result = await db.execute(select(User).where(User.id == org.owner_user_id))
    owner = owner_result.scalars().first()
    return _build_org_response(org, owner)


# --- Member Management ---

@router.get("/{org_uuid}/members", response_model=List[MemberResponse])
async def list_members(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """列出 Org 成员（含待处理邀请）"""
    org = await _get_org_or_404(db, org_uuid)

    result = await db.execute(
        select(Member, User.email, User.uuid, User.is_superuser)
        .join(User, User.id == Member.user_id)
        .where(
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    rows = result.all()
    return [
        MemberResponse(
            uuid=m.uuid, user_uuid=user_uuid, org_uuid=org.uuid,
            org_role=m.org_role, user_email=email,
            is_owner=(m.user_id == org.owner_user_id),
            is_superuser=is_su,
            invitation_status=m.invitation_status,
            created_at=m.created_at,
        )
        for m, email, user_uuid, is_su in rows
    ]


@router.post("/{org_uuid}/members", response_model=MemberResponse, status_code=status.HTTP_201_CREATED)
async def add_member(
    org_uuid: str,
    member_in: MemberAdd,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """添加成员（直接添加，仅 SystemAdmin 可用）"""
    org = await _get_org_or_404(db, org_uuid)

    # Permission: SystemAdmin only for direct add
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        raise HTTPException(status_code=403, detail="Direct member addition is restricted to system admins. Use invitations instead.")

    # Check target user exists
    target_user = await _get_user_by_uuid_or_404(db, member_in.user_uuid)

    # Check not already a member
    existing = await db.execute(
        select(Member).where(
            Member.user_id == target_user.id,
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    if existing.scalars().first():
        raise HTTPException(status_code=400, detail="User is already a member of this organization")

    member = Member(
        user_id=target_user.id,
        org_id=org.id,
        org_role=member_in.org_role,
    )
    db.add(member)
    await db.commit()
    await db.refresh(member)

    return MemberResponse(
        uuid=member.uuid, user_uuid=target_user.uuid, org_uuid=org.uuid,
        org_role=member.org_role, user_email=target_user.email, created_at=member.created_at,
    )


# --- Invitations ---

@router.post("/{org_uuid}/invitations", response_model=InvitationResponse, status_code=status.HTTP_201_CREATED)
async def invite_member(
    org_uuid: str,
    invite_in: InvitationCreate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """通过邮箱邀请用户加入组织"""
    org = await _get_org_or_404(db, org_uuid)

    # Permission: SystemAdmin or OrgAdmin+
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        member_result = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org.id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not member_result.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions to invite members")

    # Only SYSTEM org can invite superusers
    if invite_in.is_superuser and org.org_type != OrgType.SYSTEM:
        raise HTTPException(status_code=400, detail="Only the system organization can invite superusers")

    # Superuser must have Admin role
    org_role = OrgRole.ADMIN if invite_in.is_superuser else invite_in.org_role

    try:
        member = await invitation_service.create_invitation(
            db, email=invite_in.email, org=org, org_role=org_role,
            inviter=current_user, is_superuser=invite_in.is_superuser,
        )
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))

    return InvitationResponse(
        uuid=member.uuid,
        email=invite_in.email,
        org_uuid=org.uuid,
        org_name=org.name,
        org_role=member.org_role,
        status=member.invitation_status,
        inviter_email=current_user.email,
        created_at=member.created_at,
        expires_at=member.invitation_expires_at,
    )


@router.get("/{org_uuid}/invitations", response_model=List[InvitationResponse])
async def list_invitations(
    org_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """列出组织的待处理邀请"""
    org = await _get_org_or_404(db, org_uuid)

    result = await db.execute(
        select(Member, User.email.label("invited_email"))
        .join(User, User.id == Member.user_id)
        .where(
            Member.org_id == org.id,
            Member.invitation_status == InvitationStatus.PENDING,
            Member.deleted_at.is_(None),
        )
        .order_by(Member.created_at.desc())
    )
    rows = result.all()

    # Get inviter emails
    responses = []
    for member, invited_email in rows:
        inviter_email = ""
        if member.invited_by:
            inviter_result = await db.execute(select(User).where(User.id == member.invited_by))
            inviter = inviter_result.scalars().first()
            inviter_email = inviter.email if inviter else ""
        responses.append(InvitationResponse(
            uuid=member.uuid,
            email=invited_email,
            org_uuid=org.uuid,
            org_name=org.name,
            org_role=member.org_role,
            status=member.invitation_status,
            inviter_email=inviter_email,
            created_at=member.created_at,
            expires_at=member.invitation_expires_at,
        ))
    return responses


@router.delete("/{org_uuid}/invitations/{invitation_uuid}", status_code=status.HTTP_204_NO_CONTENT)
async def cancel_invitation(
    org_uuid: str,
    invitation_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """取消邀请"""
    org = await _get_org_or_404(db, org_uuid)

    # Permission: SystemAdmin or OrgAdmin+
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        member_result = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org.id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not member_result.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions")

    result = await db.execute(
        select(Member).where(
            Member.uuid == invitation_uuid,
            Member.org_id == org.id,
            Member.invitation_status == InvitationStatus.PENDING,
            Member.deleted_at.is_(None),
        )
    )
    member = result.scalars().first()
    if not member:
        raise HTTPException(status_code=404, detail="Invitation not found")

    from datetime import datetime, timezone as tz
    user_id = member.user_id
    member.invitation_status = InvitationStatus.CANCELLED
    member.deleted_at = datetime.now(tz.utc)
    await db.commit()

    # Clean up placeholder user if INVITED and no other active memberships
    from app.models.user import UserStatus
    user_result = await db.execute(select(User).where(User.id == user_id))
    user = user_result.scalars().first()
    if user and user.status == UserStatus.INVITED:
        other_members = await db.execute(
            select(Member).where(
                Member.user_id == user_id,
                Member.deleted_at.is_(None),
            )
        )
        if not other_members.scalars().first():
            await db.delete(user)
            await db.commit()


@router.patch("/{org_uuid}/members/{user_uuid}", response_model=MemberResponse)
async def update_member_role(
    org_uuid: str,
    user_uuid: str,
    member_in: MemberUpdate,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """修改成员角色"""
    org = await _get_org_or_404(db, org_uuid)
    target_user = await _get_user_by_uuid_or_404(db, user_uuid)

    # Permission check
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        my_member = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org.id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not my_member.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions")

    result = await db.execute(
        select(Member).where(
            Member.user_id == target_user.id,
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    member = result.scalars().first()
    if not member:
        raise HTTPException(status_code=404, detail="Member not found")

    member.org_role = member_in.org_role
    await db.commit()
    await db.refresh(member)

    return MemberResponse(
        uuid=member.uuid, user_uuid=target_user.uuid, org_uuid=org.uuid,
        org_role=member.org_role, user_email=target_user.email,
        created_at=member.created_at,
    )


@router.delete("/{org_uuid}/members/{user_uuid}", status_code=status.HTTP_204_NO_CONTENT)
async def remove_member(
    org_uuid: str,
    user_uuid: str,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user),
):
    """移除成员"""
    org = await _get_org_or_404(db, org_uuid)
    target_user = await _get_user_by_uuid_or_404(db, user_uuid)

    # Permission check
    if not (current_user.system_role == SystemRole.ADMIN or current_user.is_superuser):
        my_member = await db.execute(
            select(Member).where(
                Member.user_id == current_user.id,
                Member.org_id == org.id,
                Member.org_role >= OrgRole.ADMIN,
                Member.deleted_at.is_(None),
            )
        )
        if not my_member.scalars().first():
            raise HTTPException(status_code=403, detail="Not enough permissions")

    # Cannot remove the Org Owner
    if org.owner_user_id == target_user.id:
        raise HTTPException(status_code=400, detail="Cannot remove the organization owner. Transfer ownership first.")

    result = await db.execute(
        select(Member).where(
            Member.user_id == target_user.id,
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    member = result.scalars().first()
    if not member:
        raise HTTPException(status_code=404, detail="Member not found")

    from datetime import datetime, timezone
    member.deleted_at = datetime.now(timezone.utc)
    await db.commit()


@router.post("/{org_uuid}/transfer-owner", response_model=OrgResponse)
async def transfer_owner(
    org_uuid: str,
    transfer_in: TransferOwner,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_superuser),
):
    """转让 Owner（仅 SystemAdmin）"""
    org = await _get_org_or_404(db, org_uuid)
    new_owner = await _get_user_by_uuid_or_404(db, transfer_in.new_owner_uuid)

    # Verify new owner is a member
    member_result = await db.execute(
        select(Member).where(
            Member.user_id == new_owner.id,
            Member.org_id == org.id,
            Member.deleted_at.is_(None),
        )
    )
    if not member_result.scalars().first():
        raise HTTPException(status_code=400, detail="New owner must be a member of the organization")

    org.owner_user_id = new_owner.id
    await db.commit()
    await db.refresh(org)
    return _build_org_response(org, new_owner)

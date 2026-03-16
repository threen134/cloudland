import uuid as _uuid
from datetime import datetime, timedelta, timezone
from typing import Optional
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.user import User, UserStatus
from app.models.org import Organization
from app.models.member import Member, OrgRole, InvitationStatus
from app.core.security import (
    create_invitation_token,
    verify_invitation_token,
    get_password_hash,
)
from app.core.notification import send_invitation_notification
from app.core.config import settings
from app.core.logging_config import logger


class InvitationService:

    @staticmethod
    async def _get_or_create_invited_user(db: AsyncSession, email: str) -> User:
        """
        根据邮箱查找用户，如果不存在则创建一个 INVITED 状态的占位用户。
        """
        result = await db.execute(select(User).where(User.email == email))
        user = result.scalars().first()
        if user:
            return user

        # Create placeholder user with INVITED status
        user = User(
            uuid=str(_uuid.uuid4()),
            email=email,
            username=f"_inv_{_uuid.uuid4().hex[:12]}",
            hashed_password=get_password_hash(_uuid.uuid4().hex),
            is_active=False,
            status=UserStatus.INVITED,
        )
        db.add(user)
        await db.commit()
        await db.refresh(user)
        return user

    @staticmethod
    async def create_invitation(
        db: AsyncSession,
        email: str,
        org: Organization,
        org_role: int,
        inviter: User,
        is_superuser: bool = False,
    ) -> Member:
        """
        创建邀请。
        - 如果目标用户已是 org 活跃成员，拒绝。
        - 如果已有 pending 邀请，取消旧的，创建新的。
        - 发送邀请邮件。
        """
        # Get or create user
        user = await InvitationService._get_or_create_invited_user(db, email)

        # Check if user is already an active member
        existing_member = await db.execute(
            select(Member).where(
                Member.user_id == user.id,
                Member.org_id == org.id,
                Member.deleted_at.is_(None),
                # Active member: no invitation_status or already accepted
                (Member.invitation_status.is_(None)) | (Member.invitation_status == InvitationStatus.ACCEPTED),
            )
        )
        if existing_member.scalars().first():
            raise ValueError("User is already a member of this organization")

        # Cancel existing pending invitations for same user + org
        pending_result = await db.execute(
            select(Member).where(
                Member.user_id == user.id,
                Member.org_id == org.id,
                Member.invitation_status == InvitationStatus.PENDING,
                Member.deleted_at.is_(None),
            )
        )
        for old_member in pending_result.scalars().all():
            old_member.invitation_status = InvitationStatus.CANCELLED
            old_member.deleted_at = datetime.now(timezone.utc)

        # Create invitation token
        token = create_invitation_token(email, org.uuid)

        # Create member record with invitation metadata
        member = Member(
            user_id=user.id,
            org_id=org.id,
            org_role=org_role,
            invitation_token=token,
            invitation_status=InvitationStatus.PENDING,
            invited_by=inviter.id,
            invitation_expires_at=datetime.now(timezone.utc) + timedelta(hours=settings.ACTIVATION_TOKEN_EXPIRE_HOURS),
            grant_superuser=1 if is_superuser else 0,
        )
        db.add(member)
        await db.commit()
        await db.refresh(member)

        # Send notification
        is_existing = user.is_active and user.status == UserStatus.ACTIVE
        inviter_name = inviter.username or inviter.email
        await send_invitation_notification(
            email=email,
            org_name=org.name,
            inviter_name=inviter_name,
            token=token,
            is_existing_user=is_existing,
        )

        return member

    @staticmethod
    async def get_invitation_info(db: AsyncSession, token: str) -> dict:
        """
        根据 token 获取邀请信息（用于前端接受邀请页面展示）。
        """
        token_data = verify_invitation_token(token)
        if not token_data:
            raise ValueError("Invalid or expired invitation token")

        result = await db.execute(
            select(Member).where(
                Member.invitation_token == token,
                Member.invitation_status == InvitationStatus.PENDING,
                Member.deleted_at.is_(None),
            )
        )
        member = result.scalars().first()
        if not member:
            raise ValueError("Invitation not found or already used")

        if member.invitation_expires_at < datetime.now(timezone.utc):
            member.invitation_status = InvitationStatus.EXPIRED
            await db.commit()
            raise ValueError("Invitation has expired")

        # Get org info
        org_result = await db.execute(
            select(Organization).where(Organization.id == member.org_id)
        )
        org = org_result.scalars().first()

        # Get inviter info
        inviter_result = await db.execute(
            select(User).where(User.id == member.invited_by)
        )
        inviter = inviter_result.scalars().first()

        # Get invited user info
        user_result = await db.execute(
            select(User).where(User.id == member.user_id)
        )
        user = user_result.scalars().first()
        is_existing = user is not None and user.is_active and user.status == UserStatus.ACTIVE

        return {
            "email": user.email if user else "",
            "org_name": org.name if org else "",
            "org_role": member.org_role,
            "inviter_email": inviter.email if inviter else "",
            "is_existing_user": is_existing,
            "expires_at": member.invitation_expires_at,
        }

    @staticmethod
    async def accept_invitation(
        db: AsyncSession,
        token: str,
        username: Optional[str] = None,
        password: Optional[str] = None,
    ) -> dict:
        """
        接受邀请。
        - 已有活跃用户：直接激活成员关系。
        - 新用户（INVITED 状态）：设置 username + password，激活用户，再激活成员关系。
        """
        token_data = verify_invitation_token(token)
        if not token_data:
            raise ValueError("Invalid or expired invitation token")

        result = await db.execute(
            select(Member).where(
                Member.invitation_token == token,
                Member.invitation_status == InvitationStatus.PENDING,
                Member.deleted_at.is_(None),
            )
        )
        member = result.scalars().first()
        if not member:
            raise ValueError("Invitation not found or already used")

        if member.invitation_expires_at < datetime.now(timezone.utc):
            member.invitation_status = InvitationStatus.EXPIRED
            await db.commit()
            raise ValueError("Invitation has expired")

        # Get org
        org_result = await db.execute(
            select(Organization).where(
                Organization.id == member.org_id,
                Organization.deleted_at.is_(None),
            )
        )
        org = org_result.scalars().first()
        if not org:
            raise ValueError("Organization no longer exists")

        # Get user
        user_result = await db.execute(
            select(User).where(User.id == member.user_id)
        )
        user = user_result.scalars().first()
        if not user:
            raise ValueError("User not found")

        is_new_user = user.status == UserStatus.INVITED

        if user.status == UserStatus.INVITED:
            # New user: require username + password
            if not username or not password:
                raise ValueError("Username and password are required for new users")

            # Check username uniqueness
            existing_username = await db.execute(
                select(User).where(User.username == username)
            )
            if existing_username.scalars().first():
                raise ValueError("Username already taken")

            user.username = username
            user.hashed_password = get_password_hash(password)
            user.is_active = True
            user.status = UserStatus.ACTIVE
        elif not user.is_active:
            # Inactive user (e.g. disabled)
            if not username or not password:
                raise ValueError("Username and password are required for new users")
            user.username = username
            user.hashed_password = get_password_hash(password)
            user.is_active = True
            user.status = UserStatus.ACTIVE

        # Grant superuser if flagged
        if member.grant_superuser:
            user.is_superuser = True
            user.system_role = 1  # SystemRole.ADMIN

        # Mark invitation as accepted
        member.invitation_status = InvitationStatus.ACCEPTED
        await db.commit()

        logger.info(f"Invitation accepted: {user.email} joined org {org.name} as role {member.org_role}")

        return {
            "message": "Invitation accepted",
            "org_uuid": org.uuid,
            "org_name": org.name,
            "is_new_user": is_new_user,
        }


invitation_service = InvitationService()

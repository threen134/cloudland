from datetime import datetime, timedelta, timezone
from typing import Optional
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.user import User, UserStatus
from app.models.org import Organization
from app.models.member import Member, OrgRole
from app.models.invitation import Invitation, InvitationStatus
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
    async def create_invitation(
        db: AsyncSession,
        email: str,
        org: Organization,
        org_role: int,
        inviter: User,
    ) -> Invitation:
        """
        创建邀请。
        - 如果目标用户已是 org 成员，拒绝。
        - 如果已有 pending 邀请，取消旧的，创建新的。
        - 发送邀请邮件。
        """
        # Check if user already a member
        existing_user = await db.execute(
            select(User).where(User.email == email)
        )
        existing_user = existing_user.scalars().first()

        if existing_user:
            member_result = await db.execute(
                select(Member).where(
                    Member.user_id == existing_user.id,
                    Member.org_id == org.id,
                    Member.deleted_at.is_(None),
                )
            )
            if member_result.scalars().first():
                raise ValueError("User is already a member of this organization")

        # Cancel existing pending invitations for same email + org
        pending_result = await db.execute(
            select(Invitation).where(
                Invitation.email == email,
                Invitation.org_id == org.id,
                Invitation.status == InvitationStatus.PENDING,
            )
        )
        for old_inv in pending_result.scalars().all():
            old_inv.status = InvitationStatus.CANCELLED

        # Create invitation token
        token = create_invitation_token(email, org.uuid)

        # Create invitation record
        invitation = Invitation(
            email=email,
            org_id=org.id,
            org_role=org_role,
            inviter_id=inviter.id,
            token=token,
            status=InvitationStatus.PENDING,
            expires_at=datetime.now(timezone.utc) + timedelta(hours=settings.ACTIVATION_TOKEN_EXPIRE_HOURS),
        )
        db.add(invitation)
        await db.commit()
        await db.refresh(invitation)

        # Send notification
        is_existing = existing_user is not None and existing_user.is_active
        inviter_name = inviter.username or inviter.email
        await send_invitation_notification(
            email=email,
            org_name=org.name,
            inviter_name=inviter_name,
            token=token,
            is_existing_user=is_existing,
        )

        return invitation

    @staticmethod
    async def get_invitation_info(db: AsyncSession, token: str) -> dict:
        """
        根据 token 获取邀请信息（用于前端接受邀请页面展示）。
        """
        token_data = verify_invitation_token(token)
        if not token_data:
            raise ValueError("Invalid or expired invitation token")

        invitation = await db.execute(
            select(Invitation).where(
                Invitation.token == token,
                Invitation.status == InvitationStatus.PENDING,
            )
        )
        invitation = invitation.scalars().first()
        if not invitation:
            raise ValueError("Invitation not found or already used")

        if invitation.expires_at < datetime.now(timezone.utc):
            invitation.status = InvitationStatus.EXPIRED
            await db.commit()
            raise ValueError("Invitation has expired")

        # Get org info
        org_result = await db.execute(
            select(Organization).where(Organization.id == invitation.org_id)
        )
        org = org_result.scalars().first()

        # Get inviter info
        inviter_result = await db.execute(
            select(User).where(User.id == invitation.inviter_id)
        )
        inviter = inviter_result.scalars().first()

        # Check if user already exists
        user_result = await db.execute(
            select(User).where(User.email == invitation.email)
        )
        existing_user = user_result.scalars().first()
        is_existing = existing_user is not None and existing_user.is_active

        return {
            "email": invitation.email,
            "org_name": org.name if org else "",
            "org_role": invitation.org_role,
            "inviter_email": inviter.email if inviter else "",
            "is_existing_user": is_existing,
            "expires_at": invitation.expires_at,
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
        - 已有用户：直接加入 org。
        - 新用户：创建用户（需要 username + password），然后加入 org。
        """
        token_data = verify_invitation_token(token)
        if not token_data:
            raise ValueError("Invalid or expired invitation token")

        invitation = await db.execute(
            select(Invitation).where(
                Invitation.token == token,
                Invitation.status == InvitationStatus.PENDING,
            )
        )
        invitation = invitation.scalars().first()
        if not invitation:
            raise ValueError("Invitation not found or already used")

        if invitation.expires_at < datetime.now(timezone.utc):
            invitation.status = InvitationStatus.EXPIRED
            await db.commit()
            raise ValueError("Invitation has expired")

        # Get org
        org_result = await db.execute(
            select(Organization).where(
                Organization.id == invitation.org_id,
                Organization.deleted_at.is_(None),
            )
        )
        org = org_result.scalars().first()
        if not org:
            raise ValueError("Organization no longer exists")

        # Check if user already exists
        user_result = await db.execute(
            select(User).where(User.email == invitation.email)
        )
        user = user_result.scalars().first()
        is_new_user = user is None or not user.is_active

        if user and user.is_active:
            # Existing active user → just add to org
            pass
        elif user and not user.is_active:
            # Inactive user (e.g. from previous cancelled registration)
            if not username or not password:
                raise ValueError("Username and password are required for new users")
            user.username = username
            user.hashed_password = get_password_hash(password)
            user.is_active = True
            user.status = UserStatus.ACTIVE
        else:
            # Brand new user
            if not username or not password:
                raise ValueError("Username and password are required for new users")

            # Check username uniqueness
            existing_username = await db.execute(
                select(User).where(User.username == username)
            )
            if existing_username.scalars().first():
                raise ValueError("Username already taken")

            user = User(
                email=invitation.email,
                username=username,
                hashed_password=get_password_hash(password),
                is_active=True,
                status=UserStatus.ACTIVE,
            )
            db.add(user)
            await db.commit()
            await db.refresh(user)

        # Check not already a member (edge case)
        existing_member = await db.execute(
            select(Member).where(
                Member.user_id == user.id,
                Member.org_id == org.id,
                Member.deleted_at.is_(None),
            )
        )
        if existing_member.scalars().first():
            # Already a member, just mark invitation as accepted
            invitation.status = InvitationStatus.ACCEPTED
            await db.commit()
            return {"message": "Already a member", "org_uuid": org.uuid, "org_name": org.name}

        # Create membership
        member = Member(
            user_id=user.id,
            org_id=org.id,
            org_role=invitation.org_role,
        )
        db.add(member)

        # Mark invitation as accepted
        invitation.status = InvitationStatus.ACCEPTED
        await db.commit()

        logger.info(f"Invitation accepted: {invitation.email} joined org {org.name} as role {invitation.org_role}")

        return {
            "message": "Invitation accepted",
            "org_uuid": org.uuid,
            "org_name": org.name,
            "is_new_user": is_new_user,
        }


invitation_service = InvitationService()

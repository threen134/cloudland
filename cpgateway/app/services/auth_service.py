from datetime import datetime, timedelta, timezone
from typing import Optional, Dict, Any
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.future import select

from app.models.user import User, SystemRole, UserStatus
from app.models.org import Organization, OrgType
from app.models.member import Member, OrgRole
from app.models.region import Region
from app.models.token_revocation import TokenRevocation
from app.models.org_resource_quota import OrgResourceQuota
from app.models.org_resource_consumption import OrgResourceConsumption
from app.core.security import (
    get_password_hash,
    verify_password,
    create_activation_token,
    verify_activation_token,
    create_access_token,
    build_token_claims,
    verify_access_token,
)
from app.core.notification import send_activation_notification
from app.core.config import settings
from app.core.logging_config import logger


class AuthService:
    @staticmethod
    async def register_user(db: AsyncSession, user_in: Any) -> User:
        from sqlalchemy import or_

        # Check org slug uniqueness
        slug_result = await db.execute(
            select(Organization).where(
                Organization.slug == user_in.org_slug,
                Organization.deleted_at.is_(None),
            )
        )
        if slug_result.scalars().first():
            raise ValueError(f"Organization slug '{user_in.org_slug}' already exists")

        result = await db.execute(
            select(User).where(or_(User.email == user_in.email, User.username == user_in.username))
        )
        existing_user = result.scalars().first()

        target_user = None
        if existing_user:
            if existing_user.is_active:
                if existing_user.email == user_in.email:
                    raise ValueError("Email already registered")
                if existing_user.username == user_in.username:
                    raise ValueError("Username already taken")
            else:
                existing_user.email = user_in.email
                existing_user.username = user_in.username
                existing_user.language = user_in.language
                existing_user.hashed_password = get_password_hash(user_in.password)
                target_user = existing_user

        if not target_user:
            hashed_password = get_password_hash(user_in.password)
            target_user = User(
                email=user_in.email,
                username=user_in.username,
                language=user_in.language,
                hashed_password=hashed_password,
                is_active=False,
                status=UserStatus.DORMANT,
            )
            db.add(target_user)

        await db.flush()  # Get target_user.id without committing

        # Create Org with user as owner (org is ready, but user is inactive until activation)
        org = Organization(
            name=user_in.org_name,
            slug=user_in.org_slug,
            org_type=OrgType.TEAM,
            owner_user_id=target_user.id,
        )
        db.add(org)
        await db.flush()  # Get org.id without committing

        # Initialize org-region quota/consumption for all available regions
        regions_result = await db.execute(select(Region))
        for region in regions_result.scalars().all():
            db.add(OrgResourceQuota(
                org_id=org.id,
                region_id=region.id,
                max_cpu_cores=settings.DEFAULT_CPU_CORES,
                max_ram_gb=settings.DEFAULT_RAM_GB,
                max_public_ips=settings.DEFAULT_PUBLIC_IPS,
                max_disk_gb=settings.DEFAULT_DISK_GB,
            ))
            db.add(OrgResourceConsumption(
                org_id=org.id,
                region_id=region.id,
            ))

        # Add user as Admin member of the org
        member = Member(
            user_id=target_user.id,
            org_id=org.id,
            org_role=OrgRole.ADMIN,
        )
        db.add(member)

        # Single atomic commit: user + org + quota/consumption + member
        await db.commit()
        await db.refresh(target_user)

        activation_token = create_activation_token(target_user.uuid)
        await send_activation_notification(
            target_user.email, target_user.username, activation_token, language=target_user.language
        )
        return target_user

    @staticmethod
    async def activate_user(db: AsyncSession, token: str) -> Dict[str, str]:
        user_uuid = verify_activation_token(token.strip('"'))
        if user_uuid is None:
            raise ValueError("Invalid or expired activation token")

        result = await db.execute(select(User).where(User.uuid == user_uuid))
        user = result.scalars().first()
        if not user:
            raise ValueError("User not found")

        if user.is_active:
            return {"message": "Account already activated", "result": True, "status": "active"}

        user.is_active = True
        user.status = UserStatus.ACTIVE  # User has Org from registration

        await db.commit()

        return {"message": "Account activated", "result": True, "status": "active"}

    @staticmethod
    async def login_user(
        db: AsyncSession, username: str, password: str,
        org_uuid: Optional[str] = None, region: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        认证 + 签发 RS256 JWT。
        - org_uuid 未传：取用户最早加入的 Org
        - region 未传：取第一个可用的 Region
        """
        from sqlalchemy import or_
        result = await db.execute(
            select(User).where(or_(User.username == username, User.email == username))
        )
        user = result.scalars().first()

        if not user or not verify_password(password, user.hashed_password):
            raise ValueError("Incorrect username or password")

        if not user.is_active:
            raise ValueError("User account is not active")

        if user.status == UserStatus.DISABLED:
            raise PermissionError("User account is disabled")

        # Resolve Org
        org = None
        member = None
        is_owner = False
        effective_org_role = OrgRole.NONE

        if org_uuid:
            org_result = await db.execute(
                select(Organization).where(
                    Organization.uuid == org_uuid,
                    Organization.deleted_at.is_(None),
                )
            )
            org = org_result.scalars().first()
            if not org:
                raise ValueError("Organization not found")
        else:
            # Find user's first org
            member_result = await db.execute(
                select(Member).where(
                    Member.user_id == user.id,
                    Member.deleted_at.is_(None),
                ).order_by(Member.created_at.asc()).limit(1)
            )
            first_member = member_result.scalars().first()
            if first_member:
                org_result = await db.execute(
                    select(Organization).where(
                        Organization.id == first_member.org_id,
                        Organization.deleted_at.is_(None),
                    )
                )
                org = org_result.scalars().first()

        if org:
            # Verify membership (unless SystemAdmin)
            if user.system_role != SystemRole.ADMIN and not user.is_superuser:
                member_result = await db.execute(
                    select(Member).where(
                        Member.user_id == user.id,
                        Member.org_id == org.id,
                        Member.deleted_at.is_(None),
                    )
                )
                member = member_result.scalars().first()
                if not member:
                    raise PermissionError("User is not a member of this organization")
                effective_org_role = OrgRole(member.org_role)
            else:
                effective_org_role = OrgRole.ADMIN

            is_owner = (org.owner_user_id == user.id)

        # Resolve Region
        target_region = ""
        if region:
            region_result = await db.execute(
                select(Region).where(Region.name == region, Region.is_available == True)
            )
            region_obj = region_result.scalars().first()
            if not region_obj:
                raise ValueError(f"Region '{region}' not found or unavailable")
            target_region = region_obj.name
        else:
            # Get first available region
            region_result = await db.execute(
                select(Region).where(Region.is_available == True).limit(1)
            )
            region_obj = region_result.scalars().first()
            if region_obj:
                target_region = region_obj.name

        # Build and sign token
        claims = build_token_claims(
            user_uuid=user.uuid,
            email=user.email,
            org_uuid=org.uuid if org else "",
            org_name=org.name if org else "",
            region=target_region,
            system_role=user.system_role,
            org_role=int(effective_org_role),
            user_status=user.status,
            is_owner=is_owner,
        )
        access_token = create_access_token(claims)

        logger.info(f"Login: user={user.username}, org={org.name if org else 'none'}, region={target_region}")

        return {
            "access_token": access_token,
            "token_type": "bearer",
            "expires_in": settings.ACCESS_TOKEN_EXPIRE_MINUTES * 60,
            "org_uuid": org.uuid if org else None,
            "org_name": org.name if org else None,
            "region": target_region,
        }

    @staticmethod
    async def switch_org(
        db: AsyncSession, current_claims: dict,
        new_org_uuid: str, region: Optional[str] = None,
    ) -> Dict[str, Any]:
        """切换 Org（可选同时切换 Region），签发新 Token，吊销旧 Token。"""
        user_uuid = current_claims["sub"]
        old_jti = current_claims.get("jti")

        # Verify user
        user_result = await db.execute(select(User).where(User.uuid == user_uuid))
        user = user_result.scalars().first()
        if not user:
            raise ValueError("User not found")

        # Verify new org
        org_result = await db.execute(
            select(Organization).where(
                Organization.uuid == new_org_uuid,
                Organization.deleted_at.is_(None),
            )
        )
        org = org_result.scalars().first()
        if not org:
            raise ValueError("Organization not found")

        # Verify membership
        effective_org_role = OrgRole.NONE
        if user.system_role != SystemRole.ADMIN and not user.is_superuser:
            member_result = await db.execute(
                select(Member).where(
                    Member.user_id == user.id,
                    Member.org_id == org.id,
                    Member.deleted_at.is_(None),
                )
            )
            member = member_result.scalars().first()
            if not member:
                raise PermissionError("User is not a member of this organization")
            effective_org_role = OrgRole(member.org_role)
        else:
            effective_org_role = OrgRole.ADMIN

        is_owner = (org.owner_user_id == user.id)

        # Resolve region
        target_region = region or current_claims.get("region", "")
        if target_region:
            region_result = await db.execute(
                select(Region).where(Region.name == target_region, Region.is_available == True)
            )
            if not region_result.scalars().first():
                raise ValueError(f"Region '{target_region}' not found or unavailable")

        # Build new token
        claims = build_token_claims(
            user_uuid=user.uuid, email=user.email,
            org_uuid=org.uuid, org_name=org.name, region=target_region,
            system_role=user.system_role, org_role=int(effective_org_role),
            user_status=user.status, is_owner=is_owner,
        )
        access_token = create_access_token(claims)

        # Revoke old token
        if old_jti:
            old_exp = current_claims.get("exp", 0)
            revocation = TokenRevocation(
                jti=old_jti,
                expires_at=datetime.fromtimestamp(old_exp, tz=timezone.utc),
            )
            db.add(revocation)
            await db.commit()

        return {
            "access_token": access_token,
            "token_type": "bearer",
            "expires_in": settings.ACCESS_TOKEN_EXPIRE_MINUTES * 60,
            "org_uuid": org.uuid,
            "org_name": org.name,
            "region": target_region,
        }

    @staticmethod
    async def switch_region(
        db: AsyncSession, current_claims: dict, new_region: str,
    ) -> Dict[str, Any]:
        """切换 Region（Org 不变），签发新 Token，吊销旧 Token。重新查询 DB 获取最新 role/status。"""
        # Verify region
        region_result = await db.execute(
            select(Region).where(Region.name == new_region, Region.is_available == True)
        )
        if not region_result.scalars().first():
            raise ValueError(f"Region '{new_region}' not found or unavailable")

        user_uuid = current_claims["sub"]
        org_uuid = current_claims.get("org_id", "")
        old_jti = current_claims.get("jti")

        # Re-query DB for latest user status and org role (not from stale claims)
        user_result = await db.execute(select(User).where(User.uuid == user_uuid))
        user = user_result.scalars().first()
        if not user:
            raise ValueError("User not found")
        if user.status == UserStatus.DISABLED:
            raise PermissionError("User account is disabled")

        effective_org_role = OrgRole.NONE
        is_owner = False
        org_name = current_claims.get("org_name", "")

        if org_uuid:
            org_result = await db.execute(
                select(Organization).where(
                    Organization.uuid == org_uuid,
                    Organization.deleted_at.is_(None),
                )
            )
            org = org_result.scalars().first()
            if org:
                org_name = org.name
                is_owner = (org.owner_user_id == user.id)

            if user.system_role != SystemRole.ADMIN and not user.is_superuser:
                if org:
                    member_result = await db.execute(
                        select(Member).where(
                            Member.user_id == user.id,
                            Member.org_id == org.id,
                            Member.deleted_at.is_(None),
                        )
                    )
                    member = member_result.scalars().first()
                    if not member:
                        raise PermissionError("User is no longer a member of this organization")
                    effective_org_role = OrgRole(member.org_role)
            else:
                effective_org_role = OrgRole.ADMIN

        # Build new token with fresh data
        claims = build_token_claims(
            user_uuid=user.uuid, email=user.email,
            org_uuid=org_uuid, org_name=org_name,
            region=new_region,
            system_role=user.system_role,
            org_role=int(effective_org_role),
            user_status=user.status,
            is_owner=is_owner,
        )
        access_token = create_access_token(claims)

        # Revoke old token
        if old_jti:
            old_exp = current_claims.get("exp", 0)
            revocation = TokenRevocation(
                jti=old_jti,
                expires_at=datetime.fromtimestamp(old_exp, tz=timezone.utc),
            )
            db.add(revocation)
            await db.commit()

        return {
            "access_token": access_token,
            "token_type": "bearer",
            "expires_in": settings.ACCESS_TOKEN_EXPIRE_MINUTES * 60,
            "org_uuid": org_uuid,
            "org_name": org_name,
            "region": new_region,
        }

    @staticmethod
    async def revoke_token(db: AsyncSession, current_claims: dict) -> None:
        """吊销当前 Token。"""
        jti = current_claims.get("jti")
        if not jti:
            return
        exp = current_claims.get("exp", 0)
        revocation = TokenRevocation(
            jti=jti,
            expires_at=datetime.fromtimestamp(exp, tz=timezone.utc),
        )
        db.add(revocation)
        await db.commit()


auth_service = AuthService()

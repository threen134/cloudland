import bcrypt
import uuid
from datetime import datetime, timedelta, timezone
from typing import Optional
from jose import JWTError, jwt
from pydantic import BaseModel
from app.core.config import settings


def get_password_hash(password: str) -> str:
    password_bytes = password.encode('utf-8')
    salt = bcrypt.gensalt()
    hashed = bcrypt.hashpw(password_bytes, salt)
    return hashed.decode('utf-8')


def verify_password(plain_password: str, hashed_password: str) -> bool:
    password_bytes = plain_password.encode('utf-8')
    hashed_bytes = hashed_password.encode('utf-8')
    return bcrypt.checkpw(password_bytes, hashed_bytes)


# --- RS256 Key Loading ---

_private_key = None
_public_key = None


def _get_private_key():
    global _private_key
    if _private_key is None:
        key_path = settings.RSA_PRIVATE_KEY_PATH
        with open(key_path, "r") as f:
            _private_key = f.read()
    return _private_key


def _get_public_key():
    global _public_key
    if _public_key is None:
        key_path = settings.RSA_PUBLIC_KEY_PATH
        with open(key_path, "r") as f:
            _public_key = f.read()
    return _public_key


# --- Token Claims ---

class TokenClaims(BaseModel):
    sub: str           # user uuid
    email: str
    org_id: str        # org uuid (kept as 'org_id' claim name for JWT compat)
    org_name: str      # Org 名称
    region: str        # 目标 Region name
    sr: int            # SystemRole (0=User, 1=Admin)
    or_: int           # OrgRole (0-3)
    st: int            # UserStatus (1/2/3)
    is_owner: bool     # 是否为当前 Org 的 Owner
    jti: str           # 唯一 Token ID（用于吊销）
    exp: int           # 过期时间


def create_access_token(claims: TokenClaims) -> str:
    """
    使用 RS256 签发包含完整 Claims 的 JWT Access Token。
    """
    to_encode = {
        "sub": claims.sub,
        "email": claims.email,
        "org_id": claims.org_id,
        "org_name": claims.org_name,
        "region": claims.region,
        "sr": claims.sr,
        "or": claims.or_,
        "st": claims.st,
        "is_owner": claims.is_owner,
        "jti": claims.jti,
        "exp": claims.exp,
        "type": "access",
        "iss": "CloudlandCPGateway",
    }
    return jwt.encode(to_encode, _get_private_key(), algorithm="RS256")


def build_token_claims(
    user_uuid: str,
    email: str,
    org_uuid: str,
    org_name: str,
    region: str,
    system_role: int,
    org_role: int,
    user_status: int,
    is_owner: bool,
    expires_delta: Optional[timedelta] = None,
) -> TokenClaims:
    """
    构建 TokenClaims，自动生成 jti 和 exp。
    """
    if expires_delta:
        expire = datetime.now(timezone.utc) + expires_delta
    else:
        expire = datetime.now(timezone.utc) + timedelta(minutes=settings.ACCESS_TOKEN_EXPIRE_MINUTES)

    return TokenClaims(
        sub=user_uuid,
        email=email,
        org_id=org_uuid,
        org_name=org_name,
        region=region,
        sr=system_role,
        or_=org_role,
        st=user_status,
        is_owner=is_owner,
        jti=str(uuid.uuid4()),
        exp=int(expire.timestamp()),
    )


def decode_access_token(token: str) -> dict:
    """
    使用 RS256 公钥验签并解码 JWT。
    """
    return jwt.decode(token, _get_public_key(), algorithms=["RS256"])


def verify_access_token(token: str) -> Optional[dict]:
    """
    验签并返回 claims dict，失败返回 None。
    """
    try:
        payload = decode_access_token(token)
        if payload.get("type") != "access":
            return None
        return payload
    except JWTError:
        return None


# --- Activation Token (保留 HS256 用于邮箱激活，独立于主 Token 体系) ---

def create_activation_token(user_uuid: str) -> str:
    expire = datetime.now(timezone.utc) + timedelta(hours=settings.ACTIVATION_TOKEN_EXPIRE_HOURS)
    to_encode = {
        "sub": user_uuid,
        "type": "activation",
        "exp": expire
    }
    return jwt.encode(to_encode, settings.SECRET_KEY, algorithm="HS256")


def verify_activation_token(token: str) -> str | None:
    """Returns user uuid or None."""
    try:
        payload = jwt.decode(token, settings.SECRET_KEY, algorithms=["HS256"])
        user_uuid: str = payload.get("sub")
        token_type: str = payload.get("type")
        if user_uuid is None or token_type != "activation":
            return None
        return user_uuid
    except JWTError:
        return None


# --- Invitation Token ---

def create_invitation_token(email: str, org_uuid: str) -> str:
    """创建邀请令牌，包含被邀请人邮箱和组织 UUID。"""
    expire = datetime.now(timezone.utc) + timedelta(hours=settings.ACTIVATION_TOKEN_EXPIRE_HOURS)
    to_encode = {
        "sub": email,
        "org": org_uuid,
        "type": "invitation",
        "jti": str(uuid.uuid4()),
        "exp": expire,
    }
    return jwt.encode(to_encode, settings.SECRET_KEY, algorithm="HS256")


def verify_invitation_token(token: str) -> dict | None:
    """验证邀请令牌，返回 {"email": ..., "org": ...} 或 None。"""
    try:
        payload = jwt.decode(token, settings.SECRET_KEY, algorithms=["HS256"])
        if payload.get("type") != "invitation":
            return None
        email = payload.get("sub")
        org_uuid = payload.get("org")
        if not email or not org_uuid:
            return None
        return {"email": email, "org": org_uuid}
    except JWTError:
        return None

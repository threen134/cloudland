from sqlalchemy import Column, String, DateTime
from app.core.database import Base


class TokenRevocation(Base):
    """
    Token 吊销记录 (TokenRevocation)
    存储已吊销的 JWT Token ID (jti)。
    CPGateway Proxy 层验签时检查此表，命中则返回 401。
    expires_at 到期后可由定时任务清理。
    """
    __tablename__ = "token_revocations"

    jti = Column(String(64), primary_key=True)
    expires_at = Column(DateTime(timezone=True), nullable=False)

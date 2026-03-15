from app.core import security
from app.core.config import settings
from datetime import timedelta

token = security.create_access_token(
    data={"sub": "newuser", "user_id": 1},
    expires_delta=timedelta(minutes=settings.ACCESS_TOKEN_EXPIRE_MINUTES)
)
print(token)

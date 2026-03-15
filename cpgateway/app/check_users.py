
import asyncio
from app.core.database import AsyncSessionLocal
from app.models.user import User
from app.core.security import create_activation_token
from sqlalchemy import select

async def main():
    async with AsyncSessionLocal() as session:
        result = await session.execute(select(User))
        users = result.scalars().all()
        if users:
            print(f"{'ID':<5} | {'UUID':<40} | {'Username':<15} | {'Email':<25} | {'Active'}")
            print("-" * 100)
            for user in users:
                print(f"{user.id:<5} | {user.uuid:<40} | {user.username:<15} | {user.email:<25} | {user.is_active}")
        else:
            print("No users found")

if __name__ == "__main__":
    asyncio.run(main())

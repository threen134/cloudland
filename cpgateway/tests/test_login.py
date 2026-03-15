import pytest
from httpx import AsyncClient

@pytest.mark.asyncio
async def test_register_and_login(client: AsyncClient):
    # Register
    register_response = await client.post(
        "/api/v1/auth/register",
        json={
            "email": "login_test@example.com",
            "username": "logintestuser",
            "password": "securepassword"
        },
    )
    assert register_response.status_code == 201
    
    # Login
    login_response = await client.post(
        "/api/v1/auth/token",
        data={
            "username": "logintestuser",
            "password": "securepassword"
        },
        headers={"Content-Type": "application/x-www-form-urlencoded"}
    )
    assert login_response.status_code == 200
    data = login_response.json()
    assert "access_token" in data
    assert data["token_type"] == "bearer"

@pytest.mark.asyncio
async def test_login_failed(client: AsyncClient):
    login_response = await client.post(
        "/api/v1/auth/token",
        data={
            "username": "logintestuser",
            "password": "wrongpassword"
        },
        headers={"Content-Type": "application/x-www-form-urlencoded"}
    )
    assert login_response.status_code == 401

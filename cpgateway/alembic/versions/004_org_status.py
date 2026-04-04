"""org_status: add status field to organizations table

Revision ID: 004
Revises: 003
Create Date: 2026-04-04
"""
from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


revision: str = "004"
down_revision: Union[str, None] = "003"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # 存量数据默认 ACTIVE(1)，新注册走代码逻辑自动设 PENDING(0)
    op.add_column(
        "organizations",
        sa.Column("status", sa.Integer(), nullable=False, server_default="1"),
    )


def downgrade() -> None:
    op.drop_column("organizations", "status")

"""notification_channels: replace user_id with org_id for multi-tenancy

Revision ID: 002
Revises: 001
Create Date: 2026-04-01
"""
from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


revision: str = "002"
down_revision: Union[str, None] = "001"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Add org_id column (nullable first to allow data migration)
    op.add_column(
        "notification_channels",
        sa.Column("org_id", sa.BigInteger(), nullable=True),
    )

    # Migrate existing data: derive org_id from user's primary org membership
    op.execute(
        """
        UPDATE notification_channels nc
        SET org_id = m.org_id
        FROM members m
        WHERE m.user_id = nc.user_id
          AND m.id = (
              SELECT id FROM members
              WHERE user_id = nc.user_id
              ORDER BY id
              LIMIT 1
          )
        """
    )

    # For any rows still without org_id (no membership found), set to 0 as fallback
    op.execute("UPDATE notification_channels SET org_id = 0 WHERE org_id IS NULL")

    # Make org_id non-nullable
    op.alter_column("notification_channels", "org_id", nullable=False)

    # Add index on org_id
    op.create_index(
        "ix_notification_channels_org_id",
        "notification_channels",
        ["org_id"],
    )

    # Drop old user_id index and column
    op.drop_index("ix_notification_channels_user_id", table_name="notification_channels")
    op.drop_column("notification_channels", "user_id")


def downgrade() -> None:
    op.add_column(
        "notification_channels",
        sa.Column("user_id", sa.BigInteger(), nullable=True),
    )
    op.create_index(
        "ix_notification_channels_user_id",
        "notification_channels",
        ["user_id"],
    )
    op.drop_index("ix_notification_channels_org_id", table_name="notification_channels")
    op.drop_column("notification_channels", "org_id")

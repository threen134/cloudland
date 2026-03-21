"""baseline: mark existing schema as initial revision

Revision ID: 001
Revises:
Create Date: 2026-03-21
"""
from typing import Sequence, Union

revision: str = "001"
down_revision: Union[str, None] = None
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    """
    Baseline migration — all tables already exist via create_all.
    This revision just stamps the starting point for future migrations.
    """
    pass


def downgrade() -> None:
    pass

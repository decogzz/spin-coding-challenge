"""create transactions

Revision ID: 0001
Revises:
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

revision: str = "0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.create_table(
        "transactions",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column("account_id", sa.String(64), nullable=False),
        sa.Column("type", sa.Text(), nullable=False),
        sa.Column("amount", sa.Numeric(17, 2), nullable=False),
        sa.Column("currency", sa.String(3), nullable=False),
        sa.Column("description", sa.String(255), nullable=False, server_default=""),
        sa.Column("status", sa.Text(), nullable=False),
        sa.Column("provider_transaction_id", sa.Text()),
        sa.Column("balance_after", sa.Numeric(17, 2)),
        sa.Column("failure_code", sa.Text()),
        sa.Column("failure_message", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("type IN ('CREDIT', 'DEBIT')", name="transactions_type_check"),
        sa.CheckConstraint(
            "status IN ('PENDING', 'EXECUTED', 'REJECTED', 'FAILED')", name="transactions_status_check"
        ),
        sa.CheckConstraint("amount > 0", name="transactions_amount_check"),
    )
    op.create_index(
        "transactions_account_created_idx",
        "transactions",
        ["account_id", sa.text("created_at DESC"), sa.text("id DESC")],
    )
    op.create_index(
        "transactions_status_created_idx",
        "transactions",
        ["status", sa.text("created_at DESC"), sa.text("id DESC")],
    )
    op.create_index(
        "transactions_created_idx", "transactions", [sa.text("created_at DESC"), sa.text("id DESC")]
    )


def downgrade() -> None:
    op.drop_table("transactions")

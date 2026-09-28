"""Business rules checked before any transaction reaches the provider.

Each rule checks one thing. Adding a rule means writing a function and adding it
to ``DEFAULT_RULES``.
"""

from collections.abc import Callable, Sequence

from spin_transactions.domain.errors import ValidationFailedError, Violation
from spin_transactions.domain.models import ExecuteCommand, TransactionType
from spin_transactions.domain.money import (
    CURRENCY_MXN,
    MAXIMUM_DEBIT_AMOUNT,
    MAXIMUM_STORABLE_AMOUNT,
    MINIMUM_AMOUNT,
    has_cents_precision,
)

type Rule = Callable[[ExecuteCommand], Violation | None]

MAX_ACCOUNT_ID_LENGTH = 64
MAX_DESCRIPTION_LENGTH = 255


def account_id_required(command: ExecuteCommand) -> Violation | None:
    if not command.account_id:
        return Violation("accountId", "REQUIRED", "accountId is required")
    if len(command.account_id) > MAX_ACCOUNT_ID_LENGTH:
        return Violation(
            "accountId", "TOO_LONG", f"accountId must be at most {MAX_ACCOUNT_ID_LENGTH} characters"
        )
    return None


def type_supported(command: ExecuteCommand) -> Violation | None:
    if command.type not in TransactionType:
        return Violation("type", "UNSUPPORTED_TYPE", "type must be CREDIT or DEBIT")
    return None


def amount_has_cents_precision(command: ExecuteCommand) -> Violation | None:
    if not has_cents_precision(command.amount):
        return Violation("amount", "INVALID_PRECISION", "amount must have at most 2 decimal places")
    return None


def amount_above_minimum(command: ExecuteCommand) -> Violation | None:
    if command.amount.is_finite() and command.amount <= MINIMUM_AMOUNT:
        return Violation("amount", "AMOUNT_TOO_LOW", f"amount must be greater than {MINIMUM_AMOUNT}")
    return None


def amount_storable(command: ExecuteCommand) -> Violation | None:
    if command.amount.is_finite() and command.amount > MAXIMUM_STORABLE_AMOUNT:
        return Violation("amount", "AMOUNT_TOO_LARGE", f"amount must not exceed {MAXIMUM_STORABLE_AMOUNT}")
    return None


def debit_within_limit(command: ExecuteCommand) -> Violation | None:
    if (
        command.type == TransactionType.DEBIT
        and command.amount.is_finite()
        and command.amount > MAXIMUM_DEBIT_AMOUNT
    ):
        return Violation(
            "amount",
            "DEBIT_LIMIT_EXCEEDED",
            f"DEBIT amount must not exceed {MAXIMUM_DEBIT_AMOUNT} per transaction",
        )
    return None


def currency_supported(command: ExecuteCommand) -> Violation | None:
    if command.currency != CURRENCY_MXN:
        return Violation("currency", "UNSUPPORTED_CURRENCY", f"currency must be {CURRENCY_MXN}")
    return None


def description_within_limit(command: ExecuteCommand) -> Violation | None:
    if len(command.description) > MAX_DESCRIPTION_LENGTH:
        return Violation(
            "description", "TOO_LONG", f"description must be at most {MAX_DESCRIPTION_LENGTH} characters"
        )
    return None


DEFAULT_RULES: tuple[Rule, ...] = (
    account_id_required,
    type_supported,
    amount_has_cents_precision,
    amount_above_minimum,
    amount_storable,
    debit_within_limit,
    currency_supported,
    description_within_limit,
)


def check_rules(rules: Sequence[Rule], command: ExecuteCommand) -> None:
    """Run every rule and report all violations together."""
    violations = [violation for rule in rules if (violation := rule(command)) is not None]
    if violations:
        raise ValidationFailedError(violations)

"""Money is represented as ``Decimal`` with two decimal places, never as float."""

from decimal import Decimal

CENT = Decimal("0.01")
CURRENCY_MXN = "MXN"

MINIMUM_AMOUNT = Decimal("1.00")
MAXIMUM_DEBIT_AMOUNT = Decimal("10000.00")
# Largest value the NUMERIC(17, 2) column can store.
MAXIMUM_STORABLE_AMOUNT = Decimal("999999999999999.99")


def has_cents_precision(amount: Decimal) -> bool:
    """True when the amount needs no more than two decimal places (``1.500`` qualifies)."""
    return amount.is_finite() and amount == amount.quantize(CENT)


def to_money(amount: Decimal) -> Decimal:
    """Normalize to exactly two decimal places, e.g. ``1E+3`` becomes ``1000.00``."""
    return amount.quantize(CENT)

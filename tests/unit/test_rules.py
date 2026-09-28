from dataclasses import replace
from decimal import Decimal

import pytest

from spin_transactions.domain.errors import ValidationFailedError
from spin_transactions.domain.models import ExecuteCommand
from spin_transactions.domain.rules import DEFAULT_RULES, check_rules

VALID = ExecuteCommand(
    account_id="acc-123456",
    type="CREDIT",
    amount=Decimal("1500.00"),
    currency="MXN",
    description="Transferencia recibida",
)


@pytest.mark.parametrize(
    "command",
    [
        pytest.param(VALID, id="valid credit"),
        pytest.param(replace(VALID, amount=Decimal("1.01")), id="just above the minimum"),
        pytest.param(replace(VALID, type="DEBIT", amount=Decimal("10000.00")), id="debit at the limit"),
        pytest.param(replace(VALID, amount=Decimal("5000000.00")), id="credit has no business limit"),
        pytest.param(replace(VALID, amount=Decimal("1500.000")), id="trailing zeros are fine"),
        pytest.param(replace(VALID, amount=Decimal("1E+3")), id="exponent notation is fine"),
    ],
)
def test_accepts(command: ExecuteCommand) -> None:
    check_rules(DEFAULT_RULES, command)


@pytest.mark.parametrize(
    ("command", "expected_codes"),
    [
        pytest.param(
            replace(VALID, amount=Decimal("1.00")), ["AMOUNT_TOO_LOW"], id="amount equal to minimum"
        ),
        pytest.param(replace(VALID, amount=Decimal(0)), ["AMOUNT_TOO_LOW"], id="zero"),
        pytest.param(replace(VALID, amount=Decimal("-50.00")), ["AMOUNT_TOO_LOW"], id="negative"),
        pytest.param(
            replace(VALID, type="DEBIT", amount=Decimal("10000.01")),
            ["DEBIT_LIMIT_EXCEEDED"],
            id="debit above limit",
        ),
        pytest.param(replace(VALID, amount=Decimal("10.001")), ["INVALID_PRECISION"], id="three decimals"),
        pytest.param(replace(VALID, amount=Decimal("NaN")), ["INVALID_PRECISION"], id="not a number"),
        pytest.param(
            replace(VALID, amount=Decimal("1000000000000000.00")), ["AMOUNT_TOO_LARGE"], id="beyond storage"
        ),
        pytest.param(
            replace(VALID, amount=Decimal("1E+999999")), ["AMOUNT_TOO_LARGE"], id="beyond decimal precision"
        ),
        pytest.param(
            replace(VALID, amount=Decimal("5.0000000000000000000000000000001")),
            ["INVALID_PRECISION"],
            id="tiny fraction beyond decimal precision",
        ),
        pytest.param(replace(VALID, currency="USD"), ["UNSUPPORTED_CURRENCY"], id="other currency"),
        pytest.param(replace(VALID, currency="mxn"), ["UNSUPPORTED_CURRENCY"], id="lowercase currency"),
        pytest.param(replace(VALID, account_id=""), ["REQUIRED"], id="missing account"),
        pytest.param(replace(VALID, account_id="a" * 65), ["TOO_LONG"], id="account too long"),
        pytest.param(replace(VALID, type="REFUND"), ["UNSUPPORTED_TYPE"], id="unknown type"),
        pytest.param(replace(VALID, description="x" * 256), ["TOO_LONG"], id="description too long"),
        pytest.param(
            replace(VALID, account_id="", type="DEBIT", amount=Decimal("20000.00"), currency="EUR"),
            ["REQUIRED", "DEBIT_LIMIT_EXCEEDED", "UNSUPPORTED_CURRENCY"],
            id="all violations reported together",
        ),
    ],
)
def test_rejects(command: ExecuteCommand, expected_codes: list[str]) -> None:
    with pytest.raises(ValidationFailedError) as excinfo:
        check_rules(DEFAULT_RULES, command)

    assert [v.code for v in excinfo.value.violations] == expected_codes

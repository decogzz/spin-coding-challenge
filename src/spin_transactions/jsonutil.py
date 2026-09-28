"""JSON encoding that keeps money exact.

The standard library (and FastAPI/pydantic's own JSON parsing) turns JSON
numbers into floats, so ``999999999999999.99`` silently becomes ``1e15``. These
helpers read numbers as ``Decimal`` and write ``Decimal`` back as a plain JSON
number such as ``1500.00``.
"""

import json
from datetime import datetime
from decimal import Decimal
from enum import Enum
from typing import Any
from uuid import UUID

import simplejson


class JSONDecodeFailedError(ValueError):
    pass


def _reject_constant(name: str) -> Any:
    raise JSONDecodeFailedError(f"{name} is not valid JSON")


def loads(data: bytes | str) -> Any:
    """Parse JSON, reading every non-integer number as an exact Decimal."""
    try:
        return json.loads(data, parse_float=Decimal, parse_constant=_reject_constant)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise JSONDecodeFailedError(str(error)) from error


def _default(value: Any) -> Any:
    if isinstance(value, datetime):
        return value.isoformat().replace("+00:00", "Z")
    if isinstance(value, UUID):
        return str(value)
    if isinstance(value, Enum):
        return value.value
    raise TypeError(f"{type(value).__name__} is not JSON serializable")


def dumps(value: Any) -> str:
    """Serialize, writing Decimal as a JSON number with its exact digits."""
    return simplejson.dumps(value, use_decimal=True, default=_default, separators=(",", ":"))

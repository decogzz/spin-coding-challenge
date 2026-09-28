from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class Violation:
    """One invalid field or broken business rule."""

    field: str
    code: str
    message: str


class ValidationFailedError(Exception):
    """The request was refused before reaching the provider; nothing was executed or stored."""

    def __init__(self, violations: list[Violation]) -> None:
        super().__init__("; ".join(v.message for v in violations))
        self.violations = violations


class TransactionNotFoundError(Exception):
    pass


class TransactionAlreadyExistsError(Exception):
    pass


class ProviderRejectedError(Exception):
    """The provider refused the transaction for a business reason, e.g. insufficient funds."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message

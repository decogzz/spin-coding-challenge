from typing import Any

from fastapi.responses import JSONResponse

from spin_transactions import jsonutil


class ExactJSONResponse(JSONResponse):
    """Renders Decimal as an exact JSON number; FastAPI's default encoder turns it into a float."""

    def render(self, content: Any) -> bytes:
        return jsonutil.dumps(content).encode()

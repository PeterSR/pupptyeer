"""pupptyeer: convenience alias for the ``pupptyeer-client`` distribution.

Installing ``pupptyeer`` pulls in ``pupptyeer-client`` and re-exports its public
API, so both of these work:

    pip install pupptyeer

    from pupptyeer import PupptyeerClient

The actual implementation lives in the ``pupptyeer_client`` module shipped by the
``pupptyeer-client`` distribution. Prefer depending on ``pupptyeer-client``
directly; this package exists so the bare ``pupptyeer`` name resolves too.
"""
from pupptyeer_client import (
    DEFAULT_NAMESPACE,
    Cursor,
    PupptyeerClient,
    Screen,
    default_socket_path,
)

# Kept in step with the pupptyeer project release (see PROTOCOL.md / git tags).
__version__ = "0.10.0"

__all__ = [
    "PupptyeerClient",
    "Screen",
    "Cursor",
    "DEFAULT_NAMESPACE",
    "default_socket_path",
]

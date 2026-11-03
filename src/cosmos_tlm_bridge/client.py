"""Minimal, keyless OpenC3 COSMOS v5 JSON-RPC client.

Zero third-party dependencies: stdlib ``urllib`` only. Supports both auth
modes documented by ingenium-lib-cosmos:

* ``core``       — POST ``/openc3-api/auth/verify`` with ``{"password": ...}``
* ``keyless``    — no credentials at all (COSMOS Core with auth disabled,
                   or the bundled ``DROID_SIM`` reference server)

Both canonical API paths are accepted because OpenC3 deployments differ:
``/openc3-api/api`` (COSMOS Core) and ``/__openc3_api__/api`` (Enterprise /
ingress-proxied Core).
"""

from __future__ import annotations

import json
import os
import socket
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Any, Iterator

__all__ = [
    "CosmosError",
    "CosmosConnectionError",
    "CosmosTimeoutError",
    "CosmosRequestError",
    "CosmosRPCError",
    "CosmosAuthError",
    "CosmosClient",
    "DEFAULT_API_PATHS",
]

DEFAULT_API_PATHS = ("/openc3-api/api", "/__openc3_api__/api")


class CosmosError(Exception):
    """Base class for every COSMOS transport/API failure."""


class CosmosConnectionError(CosmosError):
    def __init__(self, url: str, cause: Exception | str) -> None:
        super().__init__(f"could not reach COSMOS at {url}: {cause}")
        self.url = url


class CosmosTimeoutError(CosmosError):
    def __init__(self, url: str, timeout: float) -> None:
        super().__init__(f"COSMOS request to {url} exceeded {timeout}s")
        self.url = url
        self.timeout = timeout


class CosmosRequestError(CosmosError):
    def __init__(self, url: str, status_code: int, response_text: str) -> None:
        super().__init__(f"COSMOS returned HTTP {status_code} for {url}: {response_text[:400]}")
        self.status_code = status_code
        self.response_text = response_text


class CosmosRPCError(CosmosError):
    def __init__(self, method: str, rpc_error: Any) -> None:
        super().__init__(f"COSMOS JSON-RPC {method} failed: {rpc_error}")
        self.method = method
        self.rpc_error = rpc_error


class CosmosAuthError(CosmosError):
    def __init__(self, message: str, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


@dataclass
class CosmosClient:
    """Typed COSMOS JSON-RPC client.

    >>> client = CosmosClient(base_url="http://localhost:2900")
    >>> client.tlm("DROID_SIM", "EPS", "BATT_VOLTAGE", value_type="CONVERTED")
    15.325
    """

    base_url: str = field(default_factory=lambda: os.environ.get("COSMOS_URL", "http://localhost:2900"))
    scope: str = field(default_factory=lambda: os.environ.get("COSMOS_SCOPE", "DEFAULT"))
    password: str | None = field(default_factory=lambda: os.environ.get("COSMOS_PASSWORD"))
    auth_mode: str = field(default_factory=lambda: os.environ.get("COSMOS_AUTH_MODE", "keyless"))
    api_path: str | None = None
    timeout: float = 10.0
    verify_ssl: bool = True
    _token: str | None = field(default=None, init=False, repr=False)
    _resolved_api_path: str | None = field(default=None, init=False, repr=False)
    _request_id: int = field(default=0, init=False, repr=False)
    _authed: bool = field(default=False, init=False, repr=False)

    def __post_init__(self) -> None:
        self.base_url = self.base_url.rstrip("/")
        if self.api_path:
            self._resolved_api_path = self.api_path

    # -- transport --------------------------------------------------------- #

    def _url(self, path: str) -> str:
        return f"{self.base_url}{path}"

    def _open(self, req: urllib.request.Request, timeout: float) -> tuple[int, str]:
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310
                return resp.status, resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read().decode("utf-8", "replace")
        except (TimeoutError, socket.timeout) as exc:
            raise CosmosTimeoutError(req.full_url, timeout) from exc
        except urllib.error.URLError as exc:
            if isinstance(exc.reason, (TimeoutError, socket.timeout)):
                raise CosmosTimeoutError(req.full_url, timeout) from exc
            raise CosmosConnectionError(req.full_url, exc.reason) from exc
        except OSError as exc:  # pragma: no cover - connection refused
            raise CosmosConnectionError(req.full_url, exc) from exc

    def _post(self, path: str, payload: dict[str, Any], timeout: float | None = None) -> tuple[int, str]:
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self._url(path),
            data=data,
            method="POST",
            headers={"Content-Type": "application/json", "Accept": "application/json"},
        )
        if self._token:
            req.add_header("Authorization", self._token)
        return self._open(req, timeout or self.timeout)

    # -- auth -------------------------------------------------------------- #

    def login(self) -> str | None:
        """Authenticate. Keyless mode is a no-op that returns ``None``."""
        mode = self.auth_mode.strip().lower()
        if mode == "keyless":
            self._authed = True
            return None
        if not self.password:
            raise CosmosAuthError(
                f"auth_mode={mode!r} requires COSMOS_PASSWORD; use --auth-mode keyless "
                "for an unauthenticated COSMOS Core / DROID_SIM deployment"
            )
        status, body = self._post("/openc3-api/auth/verify", {"password": self.password})
        if status in (401, 403):
            raise CosmosAuthError("COSMOS rejected the provided password", status)
        if status != 200:
            raise CosmosAuthError(f"COSMOS auth/verify returned HTTP {status}: {body[:200]}", status)
        token = _extract_token(body)
        if not token:
            raise CosmosAuthError(f"COSMOS auth/verify returned no token: {body[:200]}", status)
        self._token = token
        self._authed = True
        return token

    def _ensure_auth(self) -> None:
        if not self._authed:
            self.login()

    # -- JSON-RPC ---------------------------------------------------------- #

    def _candidate_paths(self) -> list[str]:
        if self._resolved_api_path:
            return [self._resolved_api_path]
        return list(DEFAULT_API_PATHS)

    def rpc(self, method: str, params: list[Any] | None = None, **keyword_params: Any) -> Any:
        """Issue one JSON-RPC 2.0 call and return ``result``."""
        self._ensure_auth()
        self._request_id += 1
        payload: dict[str, Any] = {
            "jsonrpc": "2.0",
            "method": method,
            "params": params or [],
            "id": self._request_id,
        }
        kw = {"scope": self.scope, **{k: v for k, v in keyword_params.items() if v is not None}}
        if kw:
            payload["keyword_params"] = kw

        last_error: Exception | None = None
        for path in self._candidate_paths():
            try:
                status, body = self._post(path, payload)
            except CosmosError as exc:  # transport-level; try the next path
                last_error = exc
                continue
            if status == 404 and not self._resolved_api_path:
                last_error = CosmosRequestError(self._url(path), status, body)
                continue
            self._resolved_api_path = path
            if status in (401, 403):
                self._token = None
                self._authed = False
                raise CosmosAuthError(f"COSMOS rejected the request: HTTP {status}", status)
            if status != 200:
                raise CosmosRequestError(self._url(path), status, body)
            try:
                doc = json.loads(body)
            except json.JSONDecodeError as exc:
                raise CosmosRequestError(self._url(path), status, body) from exc
            if "error" in doc and doc["error"] is not None:
                raise CosmosRPCError(method, doc["error"])
            return doc.get("result")
        assert last_error is not None
        raise last_error

    # -- telemetry convenience --------------------------------------------- #

    def tlm(
        self,
        target: str,
        packet: str,
        item: str,
        value_type: str = "CONVERTED",
    ) -> Any:
        """Current value of one point. ``value_type`` is RAW / CONVERTED / FORMATTED."""
        vt = str(value_type).upper()
        method = {
            "RAW": "tlm_raw",
            "CONVERTED": "tlm",
            "FORMATTED": "tlm_formatted",
        }.get(vt, "tlm")
        return self.rpc(method, [target.upper(), packet.upper(), item.upper()])

    def tlm_with_units(self, target: str, packet: str, item: str) -> str:
        return self.rpc("tlm_with_units", [target.upper(), packet.upper(), item.upper()])

    def get_tlm_packet(
        self, target: str, packet: str, value_type: str = "CONVERTED", stale_time: float = 30
    ) -> list[list[Any]]:
        """``[[ITEM, VALUE, LIMITS_STATE], ...]`` for a whole packet."""
        return self.rpc(
            "get_tlm_packet",
            [target.upper(), packet.upper()],
            type=str(value_type).upper(),
            stale_time=stale_time,
        )

    def get_tlm_values(self, items: list[list[str]], value_type: str = "CONVERTED") -> Any:
        """Batch telemetry read: ``items`` is a list of ``[TARGET, PACKET, ITEM]``."""
        return self.rpc("get_tlm_values", [items], type=str(value_type).upper())

    def get_tlm_list(self, target: str) -> list[list[Any]]:
        return self.rpc("get_tlm_list", [target.upper()])

    def get_tlm_cnt(self, target: str, packet: str) -> int:
        return self.rpc("get_tlm_cnt", [target.upper(), packet.upper()])

    def get_limits(
        self, target: str, packet: str, item: str, limits_set: str | None = None
    ) -> list[Any]:
        return self.rpc(
            "get_limits", [target.upper(), packet.upper(), item.upper(), limits_set]
        )

    def get_out_of_limits(self) -> list[list[Any]]:
        return self.rpc("get_out_of_limits", [])

    def get_overall_limits_state(self) -> str:
        return self.rpc("get_overall_limits_state", [])

    def get_server_status(self) -> dict[str, Any]:
        return self.rpc("get_server_status", [])

    def cmd(self, command_string: str, check: str = "ENABLED") -> Any:
        """Send a COSMOS command. ``check`` is ENABLED or NO_CHECK."""
        method = "cmd" if check.upper() == "ENABLED" else "cmd_no_checks"
        return self.rpc(method, [command_string])

    # -- streaming --------------------------------------------------------- #

    def stream_topic(self, target: str, packet: str) -> str:
        """OpenC3 StreamingApi topic: ``SCOPE__DECOM__{TARGET}__PACKET``."""
        return f"{self.scope}__DECOM__{{{target.upper()}}}__{packet.upper()}"

    def subscribe(
        self,
        target: str,
        packet: str,
        *,
        max_frames: int | None = None,
        timeout: float = 30.0,
    ) -> Iterator[dict[str, Any]]:
        """Consume the decommutated telemetry stream for one packet.

        Reads newline-delimited JSON from the server's streaming endpoint, which
        is the same envelope COSMOS' ActionCable ``received`` callback delivers.
        Yields raw dicts; use :meth:`frames` for typed frames.
        """
        self._ensure_auth()
        topic = self.stream_topic(target, packet)
        path = (
            "/openc3-api/stream?"
            + urllib.parse.urlencode({"topic": topic, "scope": self.scope})
        )
        req = urllib.request.Request(self._url(path), method="GET")
        if self._token:
            req.add_header("Authorization", self._token)
        deadline = time.monotonic() + timeout
        count = 0
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310
                for raw in resp:
                    if max_frames is not None and count >= max_frames:
                        return
                    if time.monotonic() > deadline:
                        return
                    line = raw.decode("utf-8", "replace").strip()
                    if not line or line.startswith(":"):
                        continue
                    try:
                        yield json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    count += 1
        except (urllib.error.URLError, OSError) as exc:
            raise CosmosConnectionError(self._url(path), exc) from exc

    def health(self) -> bool:
        """True when the server answers a trivial RPC."""
        try:
            self.rpc("get_server_status", [])
            return True
        except CosmosError:
            return False


def _extract_token(body: str) -> str | None:
    body = body.strip()
    if not body:
        return None
    try:
        doc = json.loads(body)
    except json.JSONDecodeError:
        return body.strip('"')
    if isinstance(doc, str):
        return doc
    for key in ("token", "access_token", "password", "Authorization"):
        if isinstance(doc, dict) and doc.get(key):
            return str(doc[key])
    return None

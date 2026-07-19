"""llmobs_plugin — the Python side of the plugin protocol (api/plugin/v1alpha1).

This is the reference for the language-agnostic claim: a NON-Go backend speaks the
full contract through the public protocol only. It verifies kernel-signed identity
assertions (Ed25519), holds a service token, and calls kernel primitives.

Findings surfaced building this (see docs/plugin-authors/language-agnostic-findings.md):
  * #1 token segments are UNPADDED base64url (Go RawURLEncoding); Python's
    urlsafe_b64decode requires padding — hence _b64url_decode re-pads.
  * #2 the kernel PUBLIC key must be fetched from /v1alpha1/plugin/kernel-key
    (the handshake goes kernel->plugin, so there was no other way to obtain it).
  * #3 cold-path ingest is service-token-only (no user assertion) — the plugin
    receives data from an external source directly, so there is no user context.
"""

from __future__ import annotations

import base64
import json
import time
from dataclasses import dataclass
from typing import Any

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

# httpx is imported lazily inside KernelClient so token verification (the
# language-agnostic proof) needs only `cryptography`, not the HTTP stack.

IDENTITY_ASSERTION_HEADER = "X-LLMObs-Identity-Assertion"
SERVICE_TOKEN_HEADER = "X-LLMObs-Service-Token"
_TOKEN_SCHEME = "v1"


def _b64url_decode(seg: str) -> bytes:
    # Finding #1: Go emits unpadded base64url; re-pad before decoding.
    return base64.urlsafe_b64decode(seg + "=" * (-len(seg) % 4))


class AssertionError_(Exception):
    """Raised when an identity assertion fails verification."""


class IngestError(RuntimeError):
    """Raised by ingest_otlp on a non-2xx. Carries the HTTP status so callers can
    classify permanent (4xx) vs transient (5xx) failures."""

    def __init__(self, status_code: int):
        self.status_code = status_code
        super().__init__(f"ingest failed: HTTP {status_code}")


@dataclass
class Assertion:
    sub: str
    aud: str
    project_id: str
    actor: str
    scopes: list[str]
    exp: int


def verify_assertion(token: str, kernel_pubkey: bytes, expected_aud: str, now: int | None = None) -> Assertion:
    """Verify a kernel-signed identity assertion and return its claims.

    Checks the Ed25519 signature over "v1.<payload>", the issuer, the audience
    (this plugin — token-confusion defence), and expiry.
    """
    now = now if now is not None else int(time.time())
    parts = token.split(".")
    if len(parts) != 3 or parts[0] != _TOKEN_SCHEME:
        raise AssertionError_("malformed token")
    pub = Ed25519PublicKey.from_public_bytes(kernel_pubkey)
    try:
        pub.verify(_b64url_decode(parts[2]), (parts[0] + "." + parts[1]).encode())
    except InvalidSignature as e:
        raise AssertionError_("bad signature") from e
    claims = json.loads(_b64url_decode(parts[1]))
    if claims.get("iss") != "llmobs-kernel":
        raise AssertionError_("wrong issuer")
    if expected_aud and claims.get("aud") != expected_aud:
        raise AssertionError_("wrong audience")
    if now >= int(claims.get("exp", 0)):
        raise AssertionError_("expired")
    return Assertion(
        sub=claims.get("sub", ""),
        aud=claims.get("aud", ""),
        project_id=claims.get("projectId", ""),
        actor=claims.get("actor", ""),
        scopes=list(claims.get("scopes", [])),
        exp=int(claims["exp"]),
    )


class KernelClient:
    """Calls the kernel's plugin API with this plugin's service token."""

    def __init__(self, base_url: str, service_token: str, client: Any = None):
        import httpx  # lazy: only the HTTP client path needs it

        self.base_url = base_url.rstrip("/")
        self.service_token = service_token
        self._c = client or httpx.Client(timeout=10.0)

    def fetch_kernel_pubkey(self) -> bytes:
        """Fetch the kernel Ed25519 public key (finding #2) to verify assertions."""
        r = self._c.get(f"{self.base_url}/v1alpha1/plugin/kernel-key")
        r.raise_for_status()
        return bytes.fromhex(r.json()["public_key_hex"])

    def _svc_headers(self) -> dict[str, str]:
        return {SERVICE_TOKEN_HEADER: self.service_token}

    def ingest_otlp(self, otlp_json: str) -> None:
        """Cold-path ingest: push OTLP/JSON spans (service-token-only, finding #3).

        Raises IngestError carrying the HTTP status so the caller can classify a 4xx
        (permanent: malformed OTLP the kernel will always reject) apart from a 5xx
        (transient: retry). The response body is NOT included — no data echo."""
        r = self._c.post(
            f"{self.base_url}/v1alpha1/plugin/ingest/traces",
            content=otlp_json,
            headers={**self._svc_headers(), "Content-Type": "application/json"},
        )
        if r.status_code not in (200, 202):
            raise IngestError(r.status_code)

    # --- double-token primitive smoke (kv/secrets/store need a user assertion) ---

    def _double_headers(self, assertion: str) -> dict[str, str]:
        return {SERVICE_TOKEN_HEADER: self.service_token, IDENTITY_ASSERTION_HEADER: assertion}

    def kv_set(self, assertion: str, key: str, value) -> None:
        r = self._c.post(f"{self.base_url}/v1alpha1/plugin/kv/set", json={"key": key, "value": value}, headers=self._double_headers(assertion))
        if r.status_code not in (200, 204):
            raise RuntimeError(f"kv set failed: {r.status_code} {r.text}")

    def kv_get(self, assertion: str, key: str):
        r = self._c.post(f"{self.base_url}/v1alpha1/plugin/kv/get", json={"key": key}, headers=self._double_headers(assertion))
        if r.status_code == 404:
            return None
        r.raise_for_status()
        return r.json()["value"]

    def secret_get(self, assertion: str, name: str):
        r = self._c.post(f"{self.base_url}/v1alpha1/plugin/secrets/get", json={"name": name}, headers=self._double_headers(assertion))
        if r.status_code == 404:
            return None
        r.raise_for_status()
        return r.json()["value"]

    def store_put(self, assertion: str, collection: str, id: str, record) -> None:
        r = self._c.post(
            f"{self.base_url}/v1alpha1/plugin/store/put",
            json={"collection": collection, "id": id, "record": record},
            headers=self._double_headers(assertion),
        )
        if r.status_code not in (200, 204):
            raise RuntimeError(f"store put failed: {r.status_code} {r.text}")

"""Verify an HS256 JWT and check the claims a multi-tenant service relies on. Standard library only. (For RS256 behind a JWKS use the gateway or a library;
this is the claim logic: expiry, not-before, audience, issuer, and that the tenant in the token is the tenant being asked for.)"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import time


class InvalidToken(Exception):
    pass


def _b64d(s: str) -> bytes:
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def _b64e(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def sign(claims: dict, secret: str) -> str:
    head = _b64e(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    body = _b64e(json.dumps(claims, separators=(",", ":")).encode())
    sig = _b64e(hmac.new(secret.encode(), f"{head}.{body}".encode(), hashlib.sha256).digest())
    return f"{head}.{body}.{sig}"


def verify(token: str, secret: str, *, audience: str | None = None, issuer: str | None = None, tenant: str | None = None, now: float | None = None, leeway: int = 5) -> dict:
    try:
        head_s, body_s, sig_s = token.split(".")
        head = json.loads(_b64d(head_s))
    except Exception as e:  # noqa: BLE001
        raise InvalidToken("malformed token") from e
    if head.get("alg") != "HS256":                       # never trust the header to pick a weaker or no algorithm
        raise InvalidToken("unsupported algorithm")
    good = _b64e(hmac.new(secret.encode(), f"{head_s}.{body_s}".encode(), hashlib.sha256).digest())
    if not hmac.compare_digest(good, sig_s):
        raise InvalidToken("bad signature")
    claims = json.loads(_b64d(body_s))
    t = time.time() if now is None else now
    if "exp" not in claims or t > claims["exp"] + leeway:
        raise InvalidToken("expired or no expiry")
    if "nbf" in claims and t + leeway < claims["nbf"]:
        raise InvalidToken("not yet valid")
    aud = claims.get("aud")
    if audience and audience not in (aud if isinstance(aud, list) else [aud]):
        raise InvalidToken("wrong audience")
    if issuer and claims.get("iss") != issuer:
        raise InvalidToken("wrong issuer")
    if tenant is not None and claims.get("tenant_id") != tenant:
        raise InvalidToken("token is for another tenant")
    return claims

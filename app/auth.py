import os
import base64

import jwt
from jwt import PyJWKClient
from fastapi import Header, HTTPException


def _jwks_url() -> str:
    pk = os.getenv("CLERK_PUBLISHABLE_KEY", "pk_test_c2tpbGxlZC1vcmNhLTc4LmNsZXJrLmFjY291bnRzLmRldiQ")
    b64 = pk.split("_", 2)[-1]
    b64 += "=" * (-len(b64) % 4)
    domain = base64.b64decode(b64).decode().rstrip("$")
    return f"https://{domain}/.well-known/jwks.json"


_jwks = PyJWKClient(_jwks_url(), cache_keys=True)


def get_user_id(authorization: str = Header(default=None)) -> str:
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="Authentication required.")
    token = authorization[7:]
    try:
        key = _jwks.get_signing_key_from_jwt(token)
        claims = jwt.decode(
            token, key.key,
            algorithms=["RS256"],
            options={"verify_aud": False},
        )
        return claims["sub"]
    except Exception as exc:
        raise HTTPException(status_code=401, detail="Invalid or expired token.") from exc

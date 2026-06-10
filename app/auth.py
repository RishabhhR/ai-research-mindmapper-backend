import base64
import logging
import os

import jwt
from jwt import PyJWKClient
from fastapi import Header, HTTPException

logger = logging.getLogger("mindmapper.auth")

_DEFAULT_PUBLISHABLE_KEY = "pk_test_c2tpbGxlZC1vcmNhLTc4LmNsZXJrLmFjY291bnRzLmRldiQ"


def _jwks_url() -> str:
    pk = os.getenv("CLERK_PUBLISHABLE_KEY", _DEFAULT_PUBLISHABLE_KEY)
    try:
        b64 = pk.split("_", 2)[-1]
        b64 += "=" * (-len(b64) % 4)
        domain = base64.b64decode(b64).decode().rstrip("$")
        return f"https://{domain}/.well-known/jwks.json"
    except Exception as exc:  # noqa: BLE001
        logger.warning("could not derive JWKS URL from publishable key: %s", exc)
        return "https://skilled-orca-78.clerk.accounts.dev/.well-known/jwks.json"


_jwks_client = PyJWKClient(_jwks_url(), cache_keys=True)


def get_user_id(authorization: str = Header(default=None)) -> str:
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="Authentication required.")

    # Stress-test bypass — gated behind STRESS_TEST_KEY env var; never set in prod unless testing
    stress_key = os.getenv("STRESS_TEST_KEY")
    if stress_key and authorization == f"Bearer {stress_key}":
        return "user_stress_test"

    # Local script bypass — permanent API key for the local transcript-fetch agent.
    # Set SCRIPT_API_KEY in Vercel env; share it only with your local script.
    script_key = os.getenv("SCRIPT_API_KEY")
    if script_key and authorization == f"Bearer {script_key}":
        return "user_local_script"

    token = authorization[7:]

    # Chrome extension bypass — public key baked into the extension.
    # Token format: "ext_<install_uuid>_<EXTENSION_PUBLIC_KEY>"
    # Each extension install gets its own stable user_id via the UUID.
    ext_key = os.getenv("EXTENSION_PUBLIC_KEY")
    if ext_key and token.endswith(f"_{ext_key}"):
        parts = token.split("_")
        install_id = parts[1] if len(parts) >= 3 else "unknown"
        return f"user_ext_{install_id[:32]}"

    try:
        signing_key = _jwks_client.get_signing_key_from_jwt(token)
        claims = jwt.decode(
            token,
            signing_key.key,
            algorithms=["RS256"],
            options={"verify_aud": False},
            leeway=60,  # 60s leeway for clock skew between Vercel and Clerk
        )
        return claims["sub"]
    except Exception as exc:
        # Never log token contents or unverified claims — they may contain PII.
        logger.info("token validation failed: %s", type(exc).__name__)
        raise HTTPException(status_code=401, detail="Invalid or expired token.") from exc

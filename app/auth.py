import os
import base64

import jwt
from jwt import PyJWKClient
from fastapi import Header, HTTPException


def _jwks_url() -> str:
    pk = os.getenv("CLERK_PUBLISHABLE_KEY", "pk_test_c2tpbGxlZC1vcmNhLTc4LmNsZXJrLmFjY291bnRzLmRldiQ")
    try:
        b64 = pk.split("_", 2)[-1]
        b64 += "=" * (-len(b64) % 4)
        domain = base64.b64decode(b64).decode().rstrip("$")
        url = f"https://{domain}/.well-known/jwks.json"
        print(f"DEBUG AUTH: Using JWKS URL: {url}")
        return url
    except Exception as e:
        print(f"DEBUG AUTH: Error deriving JWKS URL: {e}")
        # Fallback to hardcoded if env var is weirdly formatted
        return "https://skilled-orca-78.clerk.accounts.dev/.well-known/jwks.json"


_jwks_client = PyJWKClient(_jwks_url(), cache_keys=True)


def get_user_id(authorization: str = Header(default=None)) -> str:
    if not authorization or not authorization.startswith("Bearer "):
        print("DEBUG AUTH: Missing or invalid Authorization header")
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
        print(f"DEBUG AUTH: Token validation failed: {exc}")
        try:
            unverified = jwt.decode(token, options={"verify_signature": False})
            print(f"DEBUG AUTH: Unverified claims (for debugging): {unverified}")
        except:
            pass
        raise HTTPException(status_code=401, detail=f"Invalid or expired token: {str(exc)}") from exc

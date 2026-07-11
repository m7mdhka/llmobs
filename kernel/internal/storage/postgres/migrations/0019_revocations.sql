-- Arc O / O4 (#63) — immediate credential revocation.
-- An epoch/deny store keyed by PRINCIPAL. A kernel-signed plugin token (service token,
-- identity assertion, frontend token) is stateless (signature + TTL only), so immediate
-- revocation requires a liveness lookup at verify: a token is denied the instant its
-- principal (the deriving user, the plugin, or the token's own jti) appears here with a
-- revoked_at at or after the token was issued — on the VERY NEXT request, not at TTL.
--
-- Sessions and API keys are already DB-looked-up every request, so they are revoked by row
-- deletion (immediate); this table is the missing authority for the STATELESS signed tokens
-- and for the user cascade (revoke a user → deny every credential derived from them).
CREATE TABLE IF NOT EXISTS revocations (
    principal_kind TEXT        NOT NULL,               -- 'user' (by lower(email)) | 'plugin' (by id) | 'jti'
    principal_id   TEXT        NOT NULL,
    revoked_at     TIMESTAMPTZ NOT NULL DEFAULT now(), -- tokens issued at/before this are denied
    reason         TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (principal_kind, principal_id)
);

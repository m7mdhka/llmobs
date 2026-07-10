"""Cross-language token interop — the language-agnostic proof.

A Python backend verifies a Go-kernel-signed identity assertion (the golden vector
pinned in kernel/pkg/pluginproto/golden_test.go). Run: `python3 -m pytest` or
`python3 interop_test.py`. Requires `cryptography` (see requirements.txt).
"""

import base64
import json
import os

from llmobs_plugin import _b64url_decode, verify_assertion, AssertionError_

_GOLDEN = os.path.join(os.path.dirname(__file__), "testdata", "golden_vector.json")


def _load():
    with open(_GOLDEN) as f:
        return json.load(f)


def test_verifies_go_signed_assertion():
    v = _load()
    pub = bytes.fromhex(v["pubkey_hex"])
    a = verify_assertion(v["token"], pub, "plugin:acme/w", now=1_700_000_060)
    assert a.project_id == v["expected"]["projectId"]
    assert a.scopes == v["expected"]["scopes"]
    assert a.aud == v["expected"]["aud"]


def test_finding_1_naive_padding_fails():
    # Documents finding #1: Go's unpadded base64url breaks the naive Python decode.
    _, payload_b64, _ = _load()["token"].split(".")
    try:
        base64.urlsafe_b64decode(payload_b64)  # no re-pad
        raise AssertionError("expected padding failure")
    except Exception as e:  # binascii.Error: Incorrect padding
        assert "padding" in str(e).lower()
    # The library's re-pad helper handles it.
    assert _b64url_decode(payload_b64)


def test_wrong_audience_rejected():
    v = _load()
    try:
        verify_assertion(v["token"], bytes.fromhex(v["pubkey_hex"]), "plugin:other/x", now=1_700_000_060)
        raise AssertionError("cross-plugin assertion must be rejected")
    except AssertionError_ as e:
        assert "audience" in str(e)


def test_expired_rejected():
    v = _load()
    try:
        verify_assertion(v["token"], bytes.fromhex(v["pubkey_hex"]), "plugin:acme/w", now=1_800_000_000)
        raise AssertionError("expired assertion must be rejected")
    except AssertionError_ as e:
        assert "expired" in str(e)


def test_foreign_key_rejected():
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    v = _load()
    foreign_pub = Ed25519PrivateKey.generate().public_key().public_bytes_raw()
    try:
        verify_assertion(v["token"], foreign_pub, "plugin:acme/w", now=1_700_000_060)
        raise AssertionError("foreign-key signature must be rejected")
    except AssertionError_ as e:
        assert "signature" in str(e)


if __name__ == "__main__":
    test_verifies_go_signed_assertion()
    test_finding_1_naive_padding_fails()
    test_wrong_audience_rejected()
    test_expired_rejected()
    test_foreign_key_rejected()
    print("interop OK: Python verified the Go-signed assertion; all negative cases rejected")

import pytest

from jwt_claims import InvalidToken, sign, verify

S = "secret"
OK = {"exp": 2000, "nbf": 1000, "aud": "api", "iss": "kc", "tenant_id": "t1"}


def test_good_token_and_each_claim_check():
    assert verify(sign(OK, S), S, audience="api", issuer="kc", tenant="t1", now=1500)["tenant_id"] == "t1"
    for kw, now in (({}, 2100), ({}, 900), ({"audience": "other"}, 1500), ({"issuer": "x"}, 1500), ({"tenant": "t2"}, 1500)):
        with pytest.raises(InvalidToken):
            verify(sign(OK, S), S, now=now, **kw)


def test_tampered_signature_none_algorithm_and_garbage():
    good = sign(OK, S)
    with pytest.raises(InvalidToken):
        verify(good[:-2] + "xx", S, now=1500)
    with pytest.raises(InvalidToken):
        verify(sign(OK, "other"), S, now=1500)
    with pytest.raises(InvalidToken):
        verify("not.a.jwt", S, now=1500)
    with pytest.raises(InvalidToken):
        verify("eyJhbGciOiJub25lIn0.e30.", S, now=1500)

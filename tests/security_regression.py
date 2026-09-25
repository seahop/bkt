#!/usr/bin/env python3
"""
bkt security regression suite.

Replays the exploits from the September 2026 security review against a live
stack and asserts each one is now blocked, plus positive controls so a
"blocked" result can't come from a broken feature. Complements smoke.sh
(which covers happy paths).

Run inside the compose test image (has python3 + botocore):
  docker compose --profile test run --rm --no-deps \
    -v "$PWD/tests:/t:ro" --entrypoint python3 test /t/security_regression.py

Env: BKT_ENDPOINT, BKT_S3_ENDPOINT, BKT_USERNAME, BKT_PASSWORD, S3_BUCKETS
(optional: the linked S3-backed bucket gets the special-key/range checks too).

Creates and removes its own local buckets/users/policies; on the S3 bucket it
only touches keys under a unique e2e-sec-<ts>/ prefix. One local bucket with a
retained object is left behind (retention can't be bypassed, by design).
"""
import datetime
import hashlib
import hmac
import http.client
import json
import os
import ssl
import sys
import time
import urllib.parse
import uuid
import warnings

warnings.filterwarnings("ignore")
try:
    import botocore.session  # noqa: E402
    from botocore.config import Config  # noqa: E402
except ImportError:  # awscli v1 >= 1.4x vendors botocore
    import awscli.botocore.session as _bs  # noqa: E402
    from awscli.botocore.config import Config  # noqa: E402

    class botocore:  # noqa: N801 - shim so the rest of the file reads the same
        session = _bs

CONSOLE = os.environ.get("BKT_ENDPOINT", "https://localhost:9443").rstrip("/")
S3EP = os.environ.get("BKT_S3_ENDPOINT", "https://localhost:9000").rstrip("/")
ADMIN_USER = os.environ.get("BKT_USERNAME", "admin")
ADMIN_PASS = os.environ["BKT_PASSWORD"]
S3_BUCKET = (os.environ.get("S3_BUCKETS", "").split(",")[0]).strip()
TS = str(int(time.time()))
CTX = ssl._create_unverified_context()

passed, failed = 0, []


def check(name, cond, detail=""):
    global passed
    if cond:
        passed += 1
        print(f"  \033[32m✔\033[0m {name}")
    else:
        failed.append(name)
        print(f"  \033[31m✖\033[0m {name}  {str(detail)[:300]}")


def section(t):
    print(f"\n\033[1m── {t} ──\033[0m")


# ── HTTP helpers ─────────────────────────────────────────────────────────────
def raw(method, base, path, body=b"", headers=None):
    u = urllib.parse.urlsplit(base)
    conn = http.client.HTTPSConnection(u.hostname, u.port, context=CTX, timeout=60)
    conn.request(method, path, body=body, headers=headers or {})
    r = conn.getresponse()
    data = r.read()
    hdrs = {k.lower(): v for k, v in r.getheaders()}
    conn.close()
    return r.status, hdrs, data


def api(method, path, token=None, js=None, form=None, headers=None):
    h = dict(headers or {})
    body = b""
    if token:
        h["Authorization"] = f"Bearer {token}"
    if js is not None:
        body = json.dumps(js).encode()
        h["Content-Type"] = "application/json"
    if form is not None:
        boundary = uuid.uuid4().hex
        parts = []
        for k, v in form.items():
            if isinstance(v, tuple):  # (filename, bytes, content_type)
                fn, data, ct = v
                parts.append(
                    f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"; filename="{fn}"\r\n'
                    f"Content-Type: {ct}\r\n\r\n".encode() + data + b"\r\n")
            else:
                parts.append(
                    f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
        body = b"".join(parts) + f"--{boundary}--\r\n".encode()
        h["Content-Type"] = f"multipart/form-data; boundary={boundary}"
    st, hd, data = raw(method, CONSOLE, path, body, h)
    try:
        parsed = json.loads(data) if data else {}
    except ValueError:
        parsed = {"_raw": data[:200]}
    return st, parsed, hd


def login(user, pw):
    st, b, _ = api("POST", "/api/auth/login", js={"username": user, "password": pw})
    return b.get("token") if st == 200 else None


def new_key(token):
    st, b, _ = api("POST", "/api/access-keys", token, js={})
    return b["access_key"], b["secret_key"]


def s3client(ak, sk):
    s = botocore.session.get_session()
    return s.create_client(
        "s3", endpoint_url=S3EP, aws_access_key_id=ak, aws_secret_access_key=sk,
        region_name="us-east-1", verify=False,
        config=Config(s3={"addressing_style": "path"}, signature_version="s3v4",
                      retries={"max_attempts": 1}))


def s3err(fn, *a, **kw):
    """Run a botocore call; return (ok, error_code_or_status)."""
    try:
        fn(*a, **kw)
        return True, None
    except Exception as e:  # botocore ClientError
        resp = getattr(e, "response", {}) or {}
        return False, resp.get("Error", {}).get("Code") or resp.get(
            "ResponseMetadata", {}).get("HTTPStatusCode") or repr(e)[:120]


def create_user(admin_token, name, pw):
    st, b, _ = api("POST", "/api/users", admin_token,
                   js={"username": name, "email": f"{name}@e2e.local", "password": pw})
    return b.get("id")


def create_policy(admin_token, name, statements):
    doc = json.dumps({"Version": "2012-10-17", "Statement": statements})
    return api("POST", "/api/policies", admin_token,
               js={"name": name, "description": "e2e", "document": doc})


def attach(admin_token, uid, pid):
    return api("POST", f"/api/policies/users/{uid}/attach", admin_token, js={"policy_id": pid})


# ── Minimal SigV4 (for requests botocore won't let us malform) ───────────────
def _hm(k, m):
    return hmac.new(k, m.encode(), hashlib.sha256).digest()


def _signing_key(sk, date):
    k = _hm(("AWS4" + sk).encode(), date)
    for p in ("us-east-1", "s3", "aws4_request"):
        k = _hm(k, p)
    return k


def _enc(s, safe="-_.~"):
    return urllib.parse.quote(s, safe=safe)


def signed_put(ak, sk, bucket, key, body, content_sha, unsigned=None):
    host = urllib.parse.urlsplit(S3EP).netloc
    now = datetime.datetime.now(datetime.timezone.utc)
    amz, date = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
    path = "/" + bucket + "/" + _enc(key, "-_.~/")
    hdrs = {"host": host, "x-amz-content-sha256": content_sha, "x-amz-date": amz}
    sh = ";".join(sorted(hdrs))
    creq = "\n".join(["PUT", path, "", "".join(f"{k}:{hdrs[k]}\n" for k in sorted(hdrs)), sh, content_sha])
    scope = f"{date}/us-east-1/s3/aws4_request"
    sts = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(creq.encode()).hexdigest()])
    sig = hmac.new(_signing_key(sk, date), sts.encode(), hashlib.sha256).hexdigest()
    h = {"Host": host, "X-Amz-Content-Sha256": content_sha, "X-Amz-Date": amz,
         "Content-Length": str(len(body)),
         "Authorization": f"AWS4-HMAC-SHA256 Credential={ak}/{scope}, SignedHeaders={sh}, Signature={sig}"}
    h.update(unsigned or {})
    return raw("PUT", S3EP, path, body, h)


def presign_get(ak, sk, bucket, key, when, expires=3600):
    return presign(ak, sk, "GET", bucket, key, when, expires)


def presign(ak, sk, method, bucket, key, when, expires=3600, body=b"", send_headers=None):
    host = urllib.parse.urlsplit(S3EP).netloc
    amz, date = when.strftime("%Y%m%dT%H%M%SZ"), when.strftime("%Y%m%d")
    path = "/" + bucket + "/" + _enc(key, "-_.~/")
    q = {"X-Amz-Algorithm": "AWS4-HMAC-SHA256",
         "X-Amz-Credential": f"{ak}/{date}/us-east-1/s3/aws4_request",
         "X-Amz-Date": amz, "X-Amz-Expires": str(expires), "X-Amz-SignedHeaders": "host"}
    cq = "&".join(f"{_enc(k)}={_enc(v)}" for k, v in sorted(q.items()))
    creq = "\n".join([method, path, cq, f"host:{host}\n", "host", "UNSIGNED-PAYLOAD"])
    sts = "\n".join(["AWS4-HMAC-SHA256", amz, f"{date}/us-east-1/s3/aws4_request",
                     hashlib.sha256(creq.encode()).hexdigest()])
    sig = hmac.new(_signing_key(sk, date), sts.encode(), hashlib.sha256).hexdigest()
    return raw(method, S3EP, f"{path}?{cq}&X-Amz-Signature={sig}", body, send_headers or {})


# ── Setup ────────────────────────────────────────────────────────────────────
print(f"\033[1mbkt security regression\033[0m  console={CONSOLE} s3={S3EP} s3-bucket={S3_BUCKET or '-'}")
ADMIN = login(ADMIN_USER, ADMIN_PASS)
if not ADMIN:
    sys.exit("admin login failed")
AK, SK = new_key(ADMIN)
s3 = s3client(AK, SK)
LB = f"e2e-sec-{TS}"          # local bucket
QB = f"e2e-quota-{TS}"        # quota bucket
RB = f"e2e-ret-{TS}"          # versioned + retention bucket
for b in (LB, QB, RB):
    st, body, _ = api("POST", "/api/buckets", ADMIN, js={"name": b, "storage_backend": "local"})
    assert st == 201, (b, st, body)
cleanup_users, cleanup_policies = [], []

s3.put_object(Bucket=LB, Key="public/p1.txt", Body=b"public data")
s3.put_object(Bucket=LB, Key="secret/s1.txt", Body=b"top secret")


def s3_body(bucket, key):
    try:
        return s3.get_object(Bucket=bucket, Key=key)["Body"].read()
    except Exception:
        return None


# ── 1. MoveFolder authorization ──────────────────────────────────────────────
section("Folder move requires Get+Delete on source and Put on destination")
U, UPW = f"e2e-reader-{TS}", f"E2e!Reader1{TS}"
uid = create_user(ADMIN, U, UPW)
cleanup_users.append(uid)
st, pol, _ = create_policy(ADMIN, f"e2e-reader-{TS}", [{
    "Effect": "Allow", "Action": ["s3:GetObject", "s3:ListBucket", "s3:GetBucketLocation"],
    "Resource": [f"arn:aws:s3:::{LB}", f"arn:aws:s3:::{LB}/public/*"]}])
cleanup_policies.append(pol.get("id"))
attach(ADMIN, uid, pol.get("id"))
UT = login(U, UPW)
st, b, _ = api("POST", f"/api/buckets/{LB}/folders/move", UT,
               js={"source_prefix": "%", "destination_prefix": "x/"})
check("read-only user: LIKE-wildcard source '%' rejected", st in (400, 403), (st, b))
check("…secret/ object untouched", s3_body(LB, "secret/s1.txt") == b"top secret")
st, b, _ = api("POST", f"/api/buckets/{LB}/folders/move", UT,
               js={"source_prefix": "public/", "destination_prefix": "x/"})
check("read-only user: move of readable folder denied (no Delete/Put)", st == 403, (st, b))
check("…public/ object untouched", s3_body(LB, "public/p1.txt") == b"public data")
st, b, _ = api("POST", f"/api/buckets/{LB}/folders/move", ADMIN,
               js={"source_prefix": "public/", "destination_prefix": "moved/"})
check("admin folder move still works (control)", st == 200 and s3_body(LB, "moved/p1.txt") == b"public data", (st, b))
api("POST", f"/api/buckets/{LB}/folders/move", ADMIN, js={"source_prefix": "moved/", "destination_prefix": "public/"})

# ── 2. Key aliasing ──────────────────────────────────────────────────────────
# Keys are opaque (the local backend stores objects under the SHA-256 of the
# key): path-like spellings are distinct, valid S3 keys that never alias the
# canonical object.
section("Path-like key spellings are distinct keys (no ./ // aliasing onto real files)")
# A scratch object stands in for the canonical key, so a client library that
# resolves dot-segments in the URL path can only make this check fail, never
# damage the fixtures later sections use. ("x/../y" is valid too; it is
# covered by the Go integration tests because clients may rewrite it.)
s3.put_object(Bucket=LB, Key="alias/f.txt", Body=b"canonical")
st, b, _ = api("POST", f"/api/buckets/{LB}/objects", ADMIN,
               form={"key": "./alias/f.txt", "file": ("x.txt", b"OVERWRITTEN", "text/plain")})
check("REST upload './alias/f.txt' stored as its own key", st in (200, 201), (st, b))
ALIAS_KEYS = ("alias//f.txt", "alias/./f.txt", "/alias/f.txt")
for k in ALIAS_KEYS:
    ok, code = s3err(s3.put_object, Bucket=LB, Key=k, Body=b"OVERWRITTEN")
    check(f"S3 PutObject '{k}' stored as its own key", ok and s3_body(LB, k) == b"OVERWRITTEN", code)
check("…alias/f.txt content unchanged", s3_body(LB, "alias/f.txt") == b"canonical")
for k in ("./alias/f.txt", "alias/f.txt") + ALIAS_KEYS:
    s3err(s3.delete_object, Bucket=LB, Key=k)

# ── 3. Reserved version keyspace ─────────────────────────────────────────────
section("Reserved .bkt-versions/ prefix unreachable")
for bkt in [LB] + ([S3_BUCKET] if S3_BUCKET else []):
    ok, code = s3err(s3.put_object, Bucket=bkt, Key=f".bkt-versions/e2e-{TS}/{uuid.uuid4()}", Body=b"x")
    check(f"[{bkt}] S3 PutObject into .bkt-versions/ rejected", not ok, code)
    st, b, _ = api("POST", f"/api/buckets/{bkt}/objects", ADMIN,
                   form={"key": f".bkt-versions/e2e-{TS}/y", "file": ("y", b"x", "text/plain")})
    check(f"[{bkt}] REST upload into .bkt-versions/ rejected", st in (400, 403), (st, b))

# ── 4. Active content / security headers ─────────────────────────────────────
section("XSS hardening: console CSP and active-content download headers")
st, h, _ = raw("GET", CONSOLE, "/")
csp = h.get("content-security-policy", "")
check("console sends CSP with frame-ancestors 'none'", "frame-ancestors 'none'" in csp, csp)
check("console sends X-Frame-Options DENY + nosniff",
      h.get("x-frame-options") == "DENY" and h.get("x-content-type-options") == "nosniff", h)
s3.put_object(Bucket=LB, Key="evil.html", Body=b"<script>alert(1)</script>", ContentType="text/html")
st, h, _ = raw("GET", CONSOLE, f"/api/buckets/{LB}/objects/evil.html", headers={"Authorization": f"Bearer {ADMIN}"})
check("console object download is CSP-sandboxed", st == 200 and "sandbox" in h.get("content-security-policy", ""), (st, h.get("content-security-policy")))
r = s3.get_object(Bucket=LB, Key="evil.html")
rh = r["ResponseMetadata"]["HTTPHeaders"]
check("S3 GET of text/html forced to attachment + nosniff",
      rh.get("content-disposition", "").startswith("attachment") and rh.get("x-content-type-options") == "nosniff", rh)

# ── 5. SigV4 payload integrity ───────────────────────────────────────────────
section("SigV4: body must match signed x-amz-content-sha256")
good = b"good body"
st, _, _ = signed_put(AK, SK, LB, "hash/ok.txt", good, hashlib.sha256(good).hexdigest())
check("correctly hashed PUT accepted (control)", st == 200 and s3_body(LB, "hash/ok.txt") == good, st)
st, _, body = signed_put(AK, SK, LB, "hash/evil.txt", b"EVIL BODY", hashlib.sha256(good).hexdigest())
check("PUT with body not matching signed hash rejected", st == 400, (st, body[:200]))
check("…object not created", s3_body(LB, "hash/evil.txt") is None)
st, _, _ = signed_put(AK, SK, LB, "hash/ok.txt", b"EVIL BODY", hashlib.sha256(good).hexdigest())
check("…and existing object not overwritten", s3_body(LB, "hash/ok.txt") == good, st)

# ── 6. Presigned URL date window ─────────────────────────────────────────────
section("Presigned URLs cannot be post-dated")
now = datetime.datetime.now(datetime.timezone.utc)
st, _, _ = presign_get(AK, SK, LB, "secret/s1.txt", now)
check("presigned URL dated now works (control)", st == 200, st)
st, _, _ = presign_get(AK, SK, LB, "secret/s1.txt", now + datetime.timedelta(hours=1))
check("presigned URL dated +1h rejected", st == 403, st)
st, _, _ = presign_get(AK, SK, LB, "secret/s1.txt", now + datetime.timedelta(days=3650), 604800)
check("presigned URL dated +10y rejected", st == 403, st)

# ── 7. Special-character keys (canonical encoding + CopySource escaping) ─────
KEYS = ["My Docs/report (1).pdf", "ünï cödé.txt", "a+b%c.txt", "q?x=1&y=2 #frag.txt"]
for bkt in [LB] + ([S3_BUCKET] if S3_BUCKET else []):
    section(f"Special-character keys on {bkt}")
    for base in KEYS:
        k = f"e2e-sec-{TS}/{base}"
        data = f"payload for {base}".encode()
        ok, code = s3err(s3.put_object, Bucket=bkt, Key=k, Body=data)
        got = s3_body(bkt, k)
        listed = [o["Key"] for o in s3.list_objects_v2(Bucket=bkt, Prefix=k).get("Contents", [])] if ok else []
        ok2, code2 = s3err(s3.copy_object, Bucket=bkt, Key=k + ".copy", CopySource={"Bucket": bkt, "Key": k})
        url = s3.generate_presigned_url("get_object", Params={"Bucket": bkt, "Key": k}, ExpiresIn=300)
        pu = urllib.parse.urlsplit(url)
        pst, _, pbody = raw("GET", S3EP, pu.path + "?" + pu.query)
        check(f"{base!r}: put/get/list/copy/presign",
              ok and got == data and k in listed and ok2 and s3_body(bkt, k + ".copy") == data
              and pst == 200 and pbody == data,
              dict(put=code, get=got == data, list=k in listed, copy=code2, presign=pst))
        s3.delete_object(Bucket=bkt, Key=k)
        s3.delete_object(Bucket=bkt, Key=k + ".copy")

# ── 8. Range GET ─────────────────────────────────────────────────────────────
for bkt in [LB] + ([S3_BUCKET] if S3_BUCKET else []):
    section(f"Range GET on {bkt}")
    blob = os.urandom(300_000)
    k = f"e2e-sec-{TS}/range.bin"
    s3.put_object(Bucket=bkt, Key=k, Body=blob)
    r = s3.get_object(Bucket=bkt, Key=k, Range="bytes=123456-223455")
    check("206 with exact byte slice",
          r["ResponseMetadata"]["HTTPStatusCode"] == 206 and r["Body"].read() == blob[123456:223456])
    s3.delete_object(Bucket=bkt, Key=k)

# ── 9. Async upload overwrite ────────────────────────────────────────────────
section("Async (console >10MB path) overwrite keeps DB and bytes consistent")


def async_upload(key, data):
    st, b, _ = api("POST", f"/api/buckets/{LB}/objects/async", ADMIN,
                   form={"key": key, "file": ("f.bin", data, "application/octet-stream")})
    up = b.get("upload_id")
    for _ in range(60):
        st, s, _ = api("GET", f"/api/uploads/{up}/status", ADMIN)
        if s.get("status") in ("completed", "failed"):
            return s.get("status"), s
        time.sleep(0.5)
    return "timeout", s


a, bdata = os.urandom(200_000), os.urandom(70_000)
s1, _ = async_upload("async/ow.bin", a)
s2, info = async_upload("async/ow.bin", bdata)
check("second async upload to same key completes", s1 == "completed" and s2 == "completed", (s1, s2, info))
check("…object has second upload's bytes and size", s3_body(LB, "async/ow.bin") == bdata)

# ── 10. Quota enforcement on every write path ────────────────────────────────
section("Quota enforced on PutObject, CopyObject, multipart")
st, b, _ = api("PUT", f"/api/buckets/{QB}/settings", ADMIN, js={"quota_bytes": 1000})
check("quota set", st == 200, (st, b))
ok, _ = s3err(s3.put_object, Bucket=QB, Key="a.bin", Body=os.urandom(800))
check("800B put under 1000B quota accepted (control)", ok)
ok, code = s3err(s3.copy_object, Bucket=QB, Key="b.bin", CopySource={"Bucket": QB, "Key": "a.bin"})
check("CopyObject exceeding quota rejected", not ok, code)
ok, code = s3err(s3.put_object, Bucket=QB, Key="c.bin", Body=os.urandom(800))
check("PutObject exceeding quota rejected", not ok, code)
mpu = s3.create_multipart_upload(Bucket=QB, Key="m.bin")
okp, _ = s3err(s3.upload_part, Bucket=QB, Key="m.bin", UploadId=mpu["UploadId"], PartNumber=1, Body=os.urandom(800))
done = False
if okp:
    parts = s3.list_parts(Bucket=QB, Key="m.bin", UploadId=mpu["UploadId"])["Parts"]
    done, code = s3err(s3.complete_multipart_upload, Bucket=QB, Key="m.bin", UploadId=mpu["UploadId"],
                       MultipartUpload={"Parts": [{"PartNumber": p["PartNumber"], "ETag": p["ETag"]} for p in parts]})
check("multipart completion exceeding quota rejected", not done)
s3err(s3.abort_multipart_upload, Bucket=QB, Key="m.bin", UploadId=mpu["UploadId"])

# ── 11. Policy documents ─────────────────────────────────────────────────────
section("Policies with unsupported elements rejected")
st, b, _ = create_policy(ADMIN, f"e2e-cond-{TS}", [{
    "Effect": "Allow", "Action": ["s3:ListBucket"], "Resource": [f"arn:aws:s3:::{LB}"],
    "Condition": {"StringLike": {"s3:prefix": ["home/alice/*"]}}}])
check("Condition block rejected", st == 400, (st, b))
st, b, _ = create_policy(ADMIN, f"e2e-notaction-{TS}", [{
    "Effect": "Allow", "NotAction": ["s3:DeleteObject"], "Resource": ["*"]}])
check("NotAction rejected", st == 400, (st, b))

# ── 12. Bucket details for non-admins ────────────────────────────────────────
section("Non-admin bucket view hides webhook URL and owner record")
st, b, _ = api("PUT", f"/api/buckets/{LB}/settings", ADMIN, js={"webhook_url": "https://hooks.example.com/SECRET-TOKEN-e2e"})
check("admin can set webhook (control)", st == 200, (st, b))
for path in (f"/api/buckets/{LB}", "/api/buckets"):
    st, b, _ = api("GET", path, UT)
    blob = json.dumps(b)
    check(f"GET {path} as reader: no webhook URL / owner email",
          st == 200 and "SECRET-TOKEN" not in blob and "@e2e.local" not in blob and "admin@" not in blob, (st, blob[:300]))
api("PUT", f"/api/buckets/{LB}/settings", ADMIN, js={"webhook_url": ""})

# ── 13. Bucket-config endpoints need admin/policy, not ownership ─────────────
section("Bucket configuration requires admin or explicit permission")
st, b, _ = api("PUT", f"/api/buckets/{LB}/settings", UT, js={"replicate_to": QB})
check("reader cannot set replication", st == 403, (st, b))
st, b, _ = api("PUT", f"/api/buckets/{LB}/versioning", UT, js={"versioning": "enabled"})
check("reader cannot change versioning", st == 403, (st, b))

# ── 14. User deletion safety ─────────────────────────────────────────────────
section("Admin cannot delete themselves / last admin")
st, me, _ = api("GET", "/api/users/me", ADMIN)
st, b, _ = api("DELETE", f"/api/users/{me.get('id')}", ADMIN)
check("self-delete refused (409)", st == 409, (st, b))

section("Policy attached to a group cannot be deleted")
st, g, _ = api("POST", "/api/groups", ADMIN, js={"name": f"e2e-grp-{TS}"})
st, gp, _ = create_policy(ADMIN, f"e2e-grp-pol-{TS}", [{"Effect": "Allow", "Action": ["s3:ListBucket"], "Resource": ["*"]}])
api("POST", f"/api/groups/{g.get('id')}/policies", ADMIN, js={"policy_id": gp.get("id")})
st, b, _ = api("DELETE", f"/api/policies/{gp.get('id')}", ADMIN)
check("delete refused while attached to group (409)", st == 409, (st, b))
api("DELETE", f"/api/groups/{g.get('id')}/policies/{gp.get('id')}", ADMIN)
api("DELETE", f"/api/groups/{g.get('id')}", ADMIN)
cleanup_policies.append(gp.get("id"))

# ── 15. STS credentials die with the session ─────────────────────────────────
section("STS credentials revoked by password change")
U2, U2PW = f"e2e-sts-{TS}", f"E2e!Sts1{TS}"
uid2 = create_user(ADMIN, U2, U2PW)
cleanup_users.append(uid2)
attach(ADMIN, uid2, pol.get("id"))
U2T = login(U2, U2PW)
st, cred, _ = api("POST", "/api/sts/credentials", U2T, js={"duration_seconds": 900})
sts = s3client(cred.get("access_key"), cred.get("secret_key"))
ok, code = s3err(sts.list_objects_v2, Bucket=LB)
check("STS credentials work before password change (control)", ok, (st, code))
st, b, _ = api("PUT", "/api/users/me", U2T, js={"current_password": U2PW, "password": U2PW + "x"})
check("password changed", st == 200, (st, b))
ok, code = s3err(sts.list_objects_v2, Bucket=LB)
check("STS credentials rejected after password change", not ok, code)

# ── 16. Versioned same-key metadata copy + WORM retention ────────────────────
section("Versioned bucket: same-key REPLACE copy; retention can't be lowered")
api("PUT", f"/api/buckets/{RB}/versioning", ADMIN, js={"versioning": "enabled"})
s3.put_object(Bucket=RB, Key="doc.txt", Body=b"v1")
ok, code = s3err(s3.copy_object, Bucket=RB, Key="doc.txt", CopySource={"Bucket": RB, "Key": "doc.txt"},
                 MetadataDirective="REPLACE", Metadata={"e2e": "yes"})
head = s3.head_object(Bucket=RB, Key="doc.txt") if ok else {}
check("same-key metadata REPLACE copy works", ok and head.get("Metadata", {}).get("e2e") == "yes"
      and s3_body(RB, "doc.txt") == b"v1", code)
st, b, _ = api("PUT", f"/api/buckets/{RB}/settings", ADMIN, js={"retention_days": 1})
check("retention enabled", st == 200, (st, b))
st, b, _ = api("PUT", f"/api/buckets/{RB}/settings", ADMIN, js={"retention_days": 0})
check("retention cannot be lowered while objects are retained (409)", st == 409, (st, b))

# ── 17. S3-backed bucket creation failure is surfaced ────────────────────────
section("S3-backed bucket that the backend refuses to create is not registered")
nb = f"bkt-e2e-nocreate-{TS}"
st, b, _ = api("POST", "/api/buckets", ADMIN, js={"name": nb, "storage_backend": "s3"})
st2, _, _ = api("GET", f"/api/buckets/{nb}", ADMIN)
if st == 201:
    check("(skipped: credentials can create buckets)", True)
    api("DELETE", f"/api/buckets/{nb}", ADMIN)
else:
    check("create returns an error (not 201) and no dead bucket is left", st == 502 and st2 == 404, (st, b, st2))

# ── 18. Unsigned x-amz-* headers ─────────────────────────────────────────────
section("x-amz-* headers must be signed (upload link can't become a copy)")
now = datetime.datetime.now(datetime.timezone.utc)
st, _, _ = presign(AK, SK, "PUT", LB, "links/upload.txt", now, body=b"uploaded via link")
check("presigned PUT without extras works (control)", st == 200 and s3_body(LB, "links/upload.txt") == b"uploaded via link", st)
st, _, body = presign(AK, SK, "PUT", LB, "links/stolen.txt", now,
                      send_headers={"X-Amz-Copy-Source": f"/{LB}/secret/s1.txt"})
check("presigned PUT + unsigned X-Amz-Copy-Source rejected", st == 403 and s3_body(LB, "links/stolen.txt") is None, (st, body[:160]))
good = b"abc"
st, _, body = signed_put(AK, SK, LB, "links/inj.txt", good, hashlib.sha256(good).hexdigest(),
                         unsigned={"X-Amz-Copy-Source": f"/{LB}/secret/s1.txt"})
check("signed PUT + injected unsigned X-Amz-Copy-Source rejected", st == 403 and s3_body(LB, "links/inj.txt") is None, (st, body[:160]))

# ── 19. Folder markers & key rules per backend ───────────────────────────────
section("Folder markers (s3fs/Cyberduck mkdir) and backend-specific key rules")
ok, code = s3err(s3.put_object, Bucket=LB, Key="mk/", Body=b"")
ok2, code2 = s3err(s3.put_object, Bucket=LB, Key="mk/child.txt", Body=b"child")
check("local: 'mk/' marker and 'mk/child.txt' coexist", ok and ok2 and s3_body(LB, "mk/") == b"" and s3_body(LB, "mk/child.txt") == b"child", (code, code2))
ok, code = s3err(s3.delete_object, Bucket=LB, Key="mk/")
check("local: deleting marker leaves children", ok and s3_body(LB, "mk/child.txt") == b"child" and s3_body(LB, "mk/") is None, code)
ok, code = s3err(s3.put_object, Bucket=LB, Key="mk", Body=b"file")
check("local: object 'mk' coexists with 'mk/child.txt'", ok and s3_body(LB, "mk") == b"file" and s3_body(LB, "mk/child.txt") == b"child", code)
ok, code = s3err(s3.put_object, Bucket=LB, Key="mk/.bkt-folder", Body=b"x")
check("local: '.bkt-folder' is an ordinary key segment", ok and s3_body(LB, "mk/.bkt-folder") == b"x", code)
ok, code = s3err(s3.put_object, Bucket=LB, Key="dbl//slash.txt", Body=b"x")
check("local: 'a//b' is a valid distinct key", ok and s3_body(LB, "dbl//slash.txt") == b"x" and s3_body(LB, "dbl/slash.txt") is None, code)
for k in ("mk", "mk/.bkt-folder", "dbl//slash.txt"):
    s3err(s3.delete_object, Bucket=LB, Key=k)
if S3_BUCKET:
    k = f"e2e-sec-{TS}/dbl//slash.txt"
    ok, code = s3err(s3.put_object, Bucket=S3_BUCKET, Key=k, Body=b"s3 ok")
    check("S3-backed: 'a//b' is a valid distinct key", ok and s3_body(S3_BUCKET, k) == b"s3 ok", code)
    s3err(s3.delete_object, Bucket=S3_BUCKET, Key=k)

# ── 20. S3 CreateBucket on an existing bucket (rclone/SDK probe) ─────────────
section("S3 CreateBucket on existing bucket answers like AWS")
ok, code = s3err(s3.create_bucket, Bucket=LB)
check("existing bucket → BucketAlreadyOwnedByYou", code == "BucketAlreadyOwnedByYou", code)
ok, code = s3err(s3.create_bucket, Bucket=f"e2e-new-{TS}")
check("new bucket via S3 API still refused", not ok and code not in ("BucketAlreadyOwnedByYou",), code)

# ── 21. Versions: DeleteObjects VersionId + ListObjectVersions pagination ────
section("Version-aware DeleteObjects and paginated ListObjectVersions")
VB = f"e2e-ver-{TS}"
api("POST", "/api/buckets", ADMIN, js={"name": VB, "storage_backend": "local"})
api("PUT", f"/api/buckets/{VB}/versioning", ADMIN, js={"versioning": "enabled"})
v1 = s3.put_object(Bucket=VB, Key="k.txt", Body=b"one").get("VersionId")
s3.put_object(Bucket=VB, Key="k.txt", Body=b"two")
for i in range(3):
    s3.put_object(Bucket=VB, Key=f"p{i}.txt", Body=b"x")
page = s3.list_object_versions(Bucket=VB, MaxKeys=2)
check("ListObjectVersions honours MaxKeys and sets IsTruncated", page.get("IsTruncated") is True and
      len(page.get("Versions", [])) + len(page.get("DeleteMarkers", [])) == 2, page.get("IsTruncated"))
allv, km, vm = [], None, None
for _ in range(10):
    kw = {"Bucket": VB, "MaxKeys": 2}
    if km:
        kw.update(KeyMarker=km, VersionIdMarker=vm)
    pg = s3.list_object_versions(**kw)
    allv += [(v["Key"], v["VersionId"]) for v in pg.get("Versions", [])]
    if not pg.get("IsTruncated"):
        break
    km, vm = pg.get("NextKeyMarker"), pg.get("NextVersionIdMarker")
check("paging through versions returns every version once", len(allv) == 5 and len(set(allv)) == 5, allv)
if v1:
    r = s3.delete_objects(Bucket=VB, Delete={"Objects": [{"Key": "k.txt", "VersionId": v1}]})
    left = [v["VersionId"] for v in s3.list_object_versions(Bucket=VB, Prefix="k.txt").get("Versions", [])]
    check("DeleteObjects with VersionId removes that version (no delete marker)",
          v1 not in left and len(left) == 1 and s3_body(VB, "k.txt") == b"two" and not r.get("Errors"), (r.get("Errors"), left))
else:
    check("PutObject on versioned bucket returns VersionId", False)

# ── 22. Junk HTTP methods & webhook SSRF targets ─────────────────────────────
section("Junk HTTP methods refused; webhook URLs can't target internal hosts")
for base in (CONSOLE, S3EP):
    st, _, _ = raw("X" * 2000, base, "/")
    check(f"{base}: arbitrary method → 501", st == 501, st)
for u in ("http://127.0.0.1:9443/x", "http://169.254.169.254/latest/meta-data/", "http://localhost/",
          "http://10.0.0.5/", "http://2130706433/", "http://user:pw@example.com/"):
    st, b, _ = api("PUT", f"/api/buckets/{LB}/settings", ADMIN, js={"webhook_url": u})
    check(f"webhook_url {u} rejected", st == 400, (st, b))
st, b, _ = api("PUT", f"/api/buckets/{LB}/settings", ADMIN, js={"webhook_url": "https://hooks.example.com/ok"})
check("public webhook URL accepted (control)", st == 200, (st, b))
api("PUT", f"/api/buckets/{LB}/settings", ADMIN, js={"webhook_url": ""})

# ── Cleanup ──────────────────────────────────────────────────────────────────
for bkt in (LB, QB):
    for o in s3.list_objects_v2(Bucket=bkt).get("Contents", []):
        s3.delete_object(Bucket=bkt, Key=o["Key"])
    api("DELETE", f"/api/buckets/{bkt}", ADMIN)
try:
    vs = s3.list_object_versions(Bucket=VB)
    for v in vs.get("Versions", []) + vs.get("DeleteMarkers", []):
        s3err(s3.delete_object, Bucket=VB, Key=v["Key"], VersionId=v["VersionId"])
    for o in s3.list_objects_v2(Bucket=VB).get("Contents", []):
        s3err(s3.delete_object, Bucket=VB, Key=o["Key"])
    api("DELETE", f"/api/buckets/{VB}", ADMIN)
except Exception as e:  # cleanup is best-effort
    print("  (cleanup of", VB, "failed:", e, ")")
st, keys, _ = api("GET", "/api/access-keys", ADMIN)
for k in (keys if isinstance(keys, list) else []):
    if k.get("access_key") == AK:
        api("DELETE", f"/api/access-keys/{k.get('id')}", ADMIN)
for u in cleanup_users:
    api("DELETE", f"/api/users/{u}", ADMIN)
for p in cleanup_policies:
    api("DELETE", f"/api/policies/{p}", ADMIN)

print(f"\n\033[1mPassed: {passed}  Failed: {len(failed)}\033[0m")
for f in failed:
    print(f"  \033[31m✖\033[0m {f}")
sys.exit(1 if failed else 0)

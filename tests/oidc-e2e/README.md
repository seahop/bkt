# OIDC end-to-end test

`run.sh` proves the OpenID Connect login against a **real identity provider in
a real browser**. It starts Keycloak with `bkt-realm.json` (a PKCE-enforced
confidential client, a `groups` mapper, and three users), starts the bkt
omnibus image configured with `OIDC_*` against it, and drives Chromium through
the flow.

| User  | Groups                   | Expected              |
|-------|--------------------------|-----------------------|
| alice | bkt-admins, bkt-users    | logged in, admin      |
| bob   | bkt-users                | logged in, not admin  |
| carol | unrelated                | denied, audit-logged  |

Assertions include the PKCE `code_challenge`/`S256`, `state`, `nonce` and
`openid` scope on the authorize request, `/api/users/me` identity fields,
admin/non-admin mapping, repeat login mapping to the same account, audit log
entries with provider/groups metadata, and the denial page copy.

```bash
./tests/oidc-e2e/run.sh                                   # working tree
BKT_IMAGE=ghcr.io/seahop/bkt:latest ./tests/oidc-e2e/run.sh   # a published image
```

Everything runs on a private Docker network and is removed on exit. Container
names are lowercase on purpose: SigV4-style signatures over the `Host` header
are case-sensitive in some clients.

import json
import os
import ssl
import subprocess
from hashlib import sha256
from uuid import uuid4

import httpx

USER_URL = os.environ["DENSE_MEM_USER_URL"].rstrip("/")
CONTROL_URL = os.environ["DENSE_MEM_CONTROL_URL"].rstrip("/")
TEAM = os.environ["DENSE_MEM_E2E_TEAM_ID"]
SECRETS = [os.environ["DENSE_MEM_CONTROL_TOKEN"]]


def control(path, body=None, method=None):
    response = httpx.request(
        method or ("POST" if body is not None else "GET"),
        CONTROL_URL + "/control/api" + path,
        headers={"Authorization": "Bearer " + SECRETS[0]},
        json=body,
        timeout=30,
    )
    assert response.is_success, f"control {path}: HTTP {response.status_code}"
    return response.json()["data"]


def credential(team, binding="credential_private", scopes=None):
    result = control(
        f"/teams/{team}/credentials",
        {
            "name": "python-client-" + str(uuid4()),
            "scopes": scopes or ["read", "write"],
            "rate_limit": 1000,
            "memory_binding": binding,
        },
    )
    SECRETS.append(result["api_key"])
    return result["api_key"]


def sql(query):
    command = [
        "docker",
        "compose",
        "-p",
        os.environ["DENSE_MEM_E2E_COMPOSE_PROJECT"],
        "-f",
        os.environ["DENSE_MEM_E2E_COMPOSE_FILE"],
        "exec",
        "-T",
        "postgres",
        "sh",
        "-ec",
        'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At',
        "python-client-e2e",
    ]
    response = subprocess.run(command, input=query, text=True, capture_output=True)
    assert response.returncode == 0, "PostgreSQL fixture statement failed"
    return response.stdout.strip()


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def oauth_credentials():
    issuer = os.environ["DENSE_MEM_E2E_OAUTH_INTERNAL_URL"].rstrip("/") + "/entra"
    group, subject = "python-group-" + str(uuid4()), "python-user-" + str(uuid4())
    provider = control(
        "/sso/providers",
        {
            "name": "Python OAuth " + str(uuid4()),
            "kind": "azure_ad",
            "issuer_url": issuer,
            "tenant_id": "dense-mem-e2e-tenant",
            "identity_claim": "oid",
            "client_id": "python-client",
            "client_secret_env": "",
            "scopes": ["openid", "profile"],
            "group_claims": ["groups"],
            "groups_endpoint": "",
            "groups_scopes": [],
            "enabled": True,
            "protected_resource": {
                "enabled": True,
                "audiences": ["dense-mem-entra"],
                "jwks_source": "discovery",
                "jwks_uri": "",
                "algorithms": ["RS256"],
                "scope_claim": "scp",
                "team_claim": "dense_mem_team_id",
                "scope_mappings": [
                    {"external_scope": "memory.read", "internal_scopes": ["read"]},
                    {"external_scope": "memory.write", "internal_scopes": ["write"]},
                ],
            },
        },
    )
    control(
        f"/sso/providers/{provider['id']}/mappings",
        {
            "team_id": TEAM,
            "group_id": group,
            "group_name": group,
            "scopes": ["read", "write"],
            "role": "member",
            "enabled": True,
        },
    )
    identity, membership, owner = map(str, (uuid4(), uuid4(), uuid4()))
    session_token, csrf_token = str(uuid4()), str(uuid4())
    SECRETS.extend((session_token, csrf_token))
    sql(f"""
    BEGIN; SELECT set_config('app.tx_mode','system',true);
    INSERT INTO actor_identities (id,kind,team_id,provider,subject,display_name,active)
    VALUES ('{identity}','human',NULL,'{provider["id"]}',{literal(subject)},'Python OAuth',true);
    INSERT INTO sso_identities (id,provider_id,subject,external_id,email,display_name,active,
        last_login_at,last_entitlement_check_at)
    VALUES ('{identity}','{provider["id"]}',{literal(subject)},{literal(subject)},
        'python@example.test','Python OAuth',true,now(),now());
    INSERT INTO team_memberships (id,actor_identity_id,team_id,status,team_admin,maximum_grants,
        sso_provider_id,sso_group_id,sso_profile_name,sso_entitlement_status,
        sso_last_entitlement_checked_at,sso_last_login_at)
    VALUES ('{membership}','{identity}','{TEAM}','active',false,ARRAY['read','write'],
        '{provider["id"]}',{literal(group)},'Python OAuth','active',now(),now());
    INSERT INTO ownership_aliases
        (team_id,legacy_owner_id,canonical_identity_id,credential_id,reason)
    VALUES ('{TEAM}','{owner}','{identity}',NULL,'python_e2e');
    INSERT INTO membership_grants (membership_id,grant_name,source)
    VALUES ('{membership}','read','explicit'),('{membership}','write','explicit');
    INSERT INTO sso_entitlement_cache
        (provider_id,subject,groups,status,checked_at,expires_at,error)
    VALUES ('{provider["id"]}',{literal(subject)},ARRAY[{literal(group)}],
        'active',now(),now()+interval '8 hours','');
    INSERT INTO sso_sessions (session_hash,identity_id,provider_id,membership_id,team_id,
        csrf_hash,expires_at,created_at,last_seen_at)
    VALUES ('{sha256(session_token.encode()).hexdigest()}','{identity}','{provider["id"]}',
        '{membership}','{TEAM}','{sha256(csrf_token.encode()).hexdigest()}',
        now()+interval '8 hours',now(),now());
    SELECT set_config('app.current_team_id','{TEAM}',true);
    SELECT set_config('app.tx_mode','team',true);
    SELECT dense_mem_ensure_private_space('{TEAM}','profile_private','{identity}');
    COMMIT;
    """)
    trust = ssl.create_default_context(cafile=os.environ["NODE_EXTRA_CA_CERTS"])
    with httpx.Client(verify=trust) as client:
        response = client.post(
            os.environ["DENSE_MEM_E2E_OAUTH_MOCK_URL"].rstrip("/") + "/fixture/token",
            headers={"Authorization": "Bearer " + os.environ["DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN"]},
            json={
                "profile": "entra",
                "claims": {
                    "oid": subject,
                    "sub": subject,
                    "dense_mem_team_id": TEAM,
                    "groups": [group],
                    "scp": "memory.read memory.write",
                },
            },
        )
        assert response.is_success, "signed OAuth fixture failed"
        token = response.json()["token"]
    SECRETS.append(token)
    response = httpx.post(
        USER_URL + "/ui/api/sso/credentials",
        cookies={"dense_mem_sso_session": session_token, "dense_mem_sso_csrf": csrf_token},
        headers={"X-Dense-Mem-CSRF": csrf_token},
        json={
            "name": "Python profile-private " + str(uuid4()),
            "scopes": ["read", "write"],
            "rate_limit": 1000,
            "memory_binding": "profile_private",
        },
        timeout=30,
    )
    assert response.status_code == 201, "profile-bound API credential fixture failed"
    personal = response.json()["data"]
    assert personal["credential"]["memory_binding"] == "profile_private"
    SECRETS.append(personal["api_key"])
    return token, personal["api_key"], owner, personal["credential"]["id"]


async def raw_call(name, request, token):
    async with httpx.AsyncClient() as client:
        response = await client.post(
            USER_URL + "/mcp",
            timeout=190,
            headers={"Authorization": "Bearer " + token, "Accept": "application/json"},
            json={
                "jsonrpc": "2.0",
                "id": "fidelity",
                "method": "tools/call",
                "params": {
                    "name": name,
                    "arguments": request,
                },
            },
        )
        assert response.is_success, f"raw MCP HTTP {response.status_code}"
        value = response.json()
        assert "error" not in value
        return value["result"]["structuredContent"]


async def provider_health():
    async with httpx.AsyncClient() as client:
        response = await client.get(os.environ["DENSE_MEM_E2E_PROVIDER_URL"] + "/health")
        response.raise_for_status()
        return response.json()


def assert_no_credentials_in_logs():
    response = subprocess.run(
        [
            "docker",
            "compose",
            "-p",
            os.environ["DENSE_MEM_E2E_COMPOSE_PROJECT"],
            "-f",
            os.environ["DENSE_MEM_E2E_COMPOSE_FILE"],
            "logs",
            "--no-color",
            "server",
        ],
        text=True,
        capture_output=True,
    )
    assert response.returncode == 0
    assert all(token not in response.stdout for token in SECRETS)
    query = " OR ".join("strpos(value," + literal(token) + ")>0" for token in SECRETS[1:])
    assert (
        sql(
            "SELECT count(*) FROM (SELECT protected_resource_config::text AS value "
            "FROM sso_providers UNION ALL SELECT metadata::text FROM audit_log "
            "UNION ALL SELECT COALESCE(before_payload::text,'') FROM audit_log "
            "UNION ALL SELECT COALESCE(after_payload::text,'') FROM audit_log) persisted "
            "WHERE " + query + ";"
        )
        == "0"
    )


def write_result(receipt):
    with open(os.environ["DENSE_MEM_E2E_RESULT_FILE"], "w") as stream:
        json.dump(receipt, stream)

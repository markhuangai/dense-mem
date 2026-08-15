-- +goose Up
ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_shape_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_check;

UPDATE team_profiles
SET auth_source = 'api_key',
    sso_identity_id = NULL,
    sso_provider_id = NULL,
    sso_subject = NULL,
    sso_email = '',
    sso_group_id = '',
    sso_entitlement_status = 'unlinked',
    sso_last_entitlement_checked_at = NULL,
    sso_last_login_at = NULL
WHERE auth_source = 'hybrid'
    AND key_hash IS NOT NULL
    AND key_prefix IS NOT NULL;

UPDATE team_profiles
SET auth_source = 'sso',
    key_hash = NULL,
    key_prefix = NULL,
    key_suffix = NULL,
    sso_identity_id = CASE
        WHEN sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL THEN sso_identity_id
        ELSE NULL
    END,
    sso_provider_id = CASE
        WHEN sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL THEN sso_provider_id
        ELSE NULL
    END,
    sso_subject = COALESCE(NULLIF(sso_subject, ''), 'legacy-hybrid:' || id::text),
    sso_entitlement_status = CASE
        WHEN sso_entitlement_status IN ('active', 'denied', 'error') THEN sso_entitlement_status
        ELSE 'error'
    END
WHERE auth_source = 'hybrid';

ALTER TABLE team_profiles
    ADD CONSTRAINT team_profiles_auth_source_check
        CHECK (auth_source IN ('api_key', 'sso')),
    ADD CONSTRAINT team_profiles_auth_source_shape_check
        CHECK (
            (
                auth_source = 'api_key'
                AND key_hash IS NOT NULL
                AND key_prefix IS NOT NULL
                AND sso_identity_id IS NULL
                AND sso_provider_id IS NULL
                AND NULLIF(sso_subject, '') IS NULL
                AND sso_entitlement_status = 'unlinked'
            )
            OR (
                auth_source = 'sso'
                AND key_hash IS NULL
                AND key_prefix IS NULL
                AND NULLIF(sso_subject, '') IS NOT NULL
                AND sso_entitlement_status IN ('active', 'denied', 'error')
                AND (
                    (sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL)
                    OR (sso_identity_id IS NULL AND sso_provider_id IS NULL)
                )
            )
        );

-- +goose Down
ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_shape_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_check;

ALTER TABLE team_profiles
    ADD CONSTRAINT team_profiles_auth_source_check
        CHECK (auth_source IN ('api_key', 'sso', 'hybrid')),
    ADD CONSTRAINT team_profiles_auth_source_shape_check
        CHECK (
            (
                auth_source = 'api_key'
                AND key_hash IS NOT NULL
                AND key_prefix IS NOT NULL
                AND sso_identity_id IS NULL
                AND sso_provider_id IS NULL
                AND NULLIF(sso_subject, '') IS NULL
                AND sso_entitlement_status = 'unlinked'
            )
            OR (
                auth_source = 'sso'
                AND key_hash IS NULL
                AND key_prefix IS NULL
                AND NULLIF(sso_subject, '') IS NOT NULL
                AND sso_entitlement_status IN ('active', 'denied', 'error')
                AND (
                    (sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL)
                    OR (sso_identity_id IS NULL AND sso_provider_id IS NULL)
                )
            )
            OR (
                auth_source = 'hybrid'
                AND key_hash IS NOT NULL
                AND key_prefix IS NOT NULL
                AND NULLIF(sso_subject, '') IS NOT NULL
                AND sso_entitlement_status IN ('active', 'denied', 'error')
                AND (
                    (sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL)
                    OR (sso_identity_id IS NULL AND sso_provider_id IS NULL)
                )
            )
        );

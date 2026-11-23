-- Phase 9: accounts from Authentik (OIDC). The subject is the stable id; name and role are refreshed at every
-- login from the claims (role from the groups relay-admin/-operator/-viewer).

ALTER TABLE user ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE user ADD COLUMN email TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX user_oidc_subject ON user(subject) WHERE kind = 'oidc';

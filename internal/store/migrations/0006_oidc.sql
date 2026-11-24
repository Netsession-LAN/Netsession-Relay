-- Accounts from single sign-on (OIDC). The subject is the stable id; name and role are refreshed at every
-- login from the claims (role from the groups of the account).

ALTER TABLE user ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE user ADD COLUMN email TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX user_oidc_subject ON user(subject) WHERE kind = 'oidc';

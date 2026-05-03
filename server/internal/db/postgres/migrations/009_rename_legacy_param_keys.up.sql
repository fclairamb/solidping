-- Rename legacy top-level snake_case system parameter keys to the
-- dot-namespaced convention used everywhere else. System parameters
-- live with organization_uid IS NULL; per-org rows (if anyone has
-- happened to set one of these names per-org) are left alone.
update parameters set key = 'auth.jwt_secret'      where key = 'jwt_secret'    and organization_uid is null;
update parameters set key = 'server.job_workers'   where key = 'job_workers'   and organization_uid is null;
update parameters set key = 'server.check_workers' where key = 'check_workers' and organization_uid is null;
update parameters set key = 'server.base_url'      where key = 'base_url'      and organization_uid is null;
update parameters set key = 'node.role'            where key = 'node_role'     and organization_uid is null;
update parameters set key = 'node.region'          where key = 'node_region'   and organization_uid is null;
update parameters set key = 'email.inbox'          where key = 'email_inbox'   and organization_uid is null;

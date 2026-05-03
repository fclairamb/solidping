-- Reverse the rename: restore the legacy top-level snake_case keys.
update parameters set key = 'jwt_secret'    where key = 'auth.jwt_secret'      and organization_uid is null;
update parameters set key = 'job_workers'   where key = 'server.job_workers'   and organization_uid is null;
update parameters set key = 'check_workers' where key = 'server.check_workers' and organization_uid is null;
update parameters set key = 'base_url'      where key = 'server.base_url'      and organization_uid is null;
update parameters set key = 'node_role'     where key = 'node.role'            and organization_uid is null;
update parameters set key = 'node_region'   where key = 'node.region'          and organization_uid is null;
update parameters set key = 'email_inbox'   where key = 'email.inbox'          and organization_uid is null;

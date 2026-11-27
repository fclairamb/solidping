-- v0.57.0 — postgres rollback (parity only; downs are never run in production).

-- SECTION: worker-capabilities-unique
alter table workers drop constraint if exists workers_capabilities_shape;

--bun:split

alter table workers add constraint workers_capabilities_shape check (
  capabilities is null
  or (
    capabilities::text ~ '^\{([a-z0-9-]+(,[a-z0-9-]+)*)?\}$'
    and capabilities::text !~ '(\{|,)([a-z0-9-]+)(,[a-z0-9-]+)*,\2(,|\})'
  )
);

--bun:split

drop function if exists workers_capabilities_unique(text[]);

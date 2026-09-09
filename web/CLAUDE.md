The two apps here are `dash0` (the operator dashboard, served at `/d`) and
`status0` (the public status page, served at `/s`). The legacy `dash` app was
deleted in spec 2026-09-09-01.

Neither app writes its base path out by hand: dash0 reads `DASH_BASE` /
`STATUS_BASE` from `src/lib/base-path.ts`, both e2e suites export the prefix
from their `e2e/fixtures.ts`, and the backend side of the same values lives in
`config.DashboardBasePath` / `config.StatusBasePath`.

For fast testing, including end-to-end tests, you can sue `make dev-test`, it will rebuild and restart the
server at each go file change and update the frontend app at each change. That way you can change files and
instantly see the results.

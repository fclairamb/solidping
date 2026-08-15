import type { CheckType } from "@/components/checks/form/types/common";

// checkTypeDocsAnchors maps each monitorable check type to its explicit
// heading anchor in web/docs/docs/features/check-types.md
// (`### Heading {#anchor}`). Anchors are pinned to the slug Docusaurus
// auto-generated for that heading *before* the ID was pinned, computed with
// the actual `github-slugger` package so already-published deep links keep
// working — see the spec for
// "Check edit page: docs link is generic, domain expiration has no
// warning/critical days, and periods stop at 24h".
//
// `sleep` is a synthetic/testing check type (not customer-facing, see
// server/internal/checkers/CLAUDE.md) and has no docs section — it is
// intentionally omitted here.
//
// KEEP THIS IN SYNC with the check-type registry
// (server/internal/checkers/checkerdef/types.go) and the markdown headings.
// server/internal/checkers/registry/docs_anchor_test.go fails the build if
// this map drifts from either.
export const checkTypeDocsAnchors: Partial<Record<CheckType, string>> = {
  http: "httphttps",
  tcp: "tcp",
  udp: "udp",
  icmp: "icmp-ping",
  dns: "dns",
  websocket: "websocket",
  rdp: "rdp-remote-desktop",
  ssl: "ssltls-certificate",
  domain: "domain-expiration",
  dnsbl: "dnsbl-dns-blocklist",
  postgresql: "postgresql",
  mysql: "mysql--mariadb",
  mongodb: "mongodb",
  redis: "redis",
  mssql: "microsoft-sql-server",
  oracle: "oracle-database",
  clickhouse: "clickhouse",
  smtp: "smtp",
  imap: "imap",
  pop3: "pop3",
  email: "email-reception-passive-inbox",
  ssh: "ssh",
  ftp: "ftp",
  sftp: "sftp",
  grpc: "grpc",
  kafka: "kafka",
  rabbitmq: "rabbitmq",
  mqtt: "mqtt",
  sip: "sip-voip",
  ntp: "ntp-time-server",
  snmp: "snmp",
  docker: "docker",
  a2s: "a2s-game-server-source--steam",
  minecraft: "minecraft",
  freebox_line: "freebox-line-xdsl--ftth",
  kubernetes: "kubernetes-workload-replica-health",
  heartbeat: "heartbeat",
  js: "javascript",
  browser: "browser",
};

const checkTypesDocsBaseHref = "/docs/features/check-types";

/**
 * docsHrefForType returns the per-type deep link into the check-types docs
 * page, falling back to the bare page for unknown/unmapped types (e.g. the
 * synthetic `sleep` type).
 */
export function docsHrefForType(type: string | undefined): string {
  const anchor = type ? checkTypeDocsAnchors[type as CheckType] : undefined;
  return anchor ? `${checkTypesDocsBaseHref}#${anchor}` : checkTypesDocsBaseHref;
}

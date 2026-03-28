import { useMemo } from "react";
import { FileText } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { CheckTypeSamples, SampleConfig } from "@/api/hooks";
import { useSampleConfigs } from "@/api/hooks";

interface SampleConfigPickerProps {
  onSelect: (checkType: string, sample: SampleConfig) => void;
  onSkip: () => void;
}

function formatPeriodLabel(seconds: number): string {
  if (seconds >= 86400) return `${seconds / 86400}d`;
  if (seconds >= 3600) return `${seconds / 3600}h`;
  if (seconds >= 60) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function configSummary(checkType: string, config: Record<string, unknown>): string {
  const url = config.url as string | undefined;
  if (url) {
    try {
      const parsed = new URL(url);
      return parsed.hostname + (parsed.pathname !== "/" ? parsed.pathname : "");
    } catch {
      return url;
    }
  }

  const host = config.host as string | undefined;
  const port = config.port as number | undefined;
  if (host && port) return `${host}:${port}`;
  if (host) return host;

  const domain = config.domain as string | undefined;
  if (domain) return domain;

  const containerName = config.containerName as string | undefined;
  if (containerName) return containerName;

  return checkType;
}

const typeLabels: Record<string, string> = {
  http: "HTTP",
  tcp: "TCP",
  icmp: "ICMP",
  dns: "DNS",
  ssl: "SSL",
  heartbeat: "Heartbeat",
  domain: "Domain",
  smtp: "SMTP",
  udp: "UDP",
  ssh: "SSH",
  pop3: "POP3",
  imap: "IMAP",
  websocket: "WebSocket",
  postgresql: "PostgreSQL",
  mysql: "MySQL",
  redis: "Redis",
  mongodb: "MongoDB",
  ftp: "FTP",
  sftp: "SFTP",
  js: "JavaScript",
  mssql: "MSSQL",
  oracle: "Oracle",
  grpc: "gRPC",
  kafka: "Kafka",
  mqtt: "MQTT",
  gameserver: "Game Server",
  rabbitmq: "RabbitMQ",
  snmp: "SNMP",
  docker: "Docker",
  browser: "Browser",
};

export function SampleConfigPicker({ onSelect, onSkip }: SampleConfigPickerProps) {
  const { data: sampleGroups, isLoading } = useSampleConfigs();

  const flatSamples = useMemo(() => {
    if (!sampleGroups) return [];
    return sampleGroups.flatMap((group: CheckTypeSamples) =>
      group.samples.map((sample: SampleConfig) => ({
        checkType: group.checkType,
        ...sample,
      }))
    );
  }, [sampleGroups]);

  if (isLoading) {
    return (
      <div className="text-center py-8 text-muted-foreground text-sm">
        Loading templates...
      </div>
    );
  }

  if (flatSamples.length === 0) return null;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-lg font-semibold">Start from a template</h3>
          <p className="text-sm text-muted-foreground">
            Pick a pre-configured check or start from scratch
          </p>
        </div>
        <button
          type="button"
          onClick={onSkip}
          className="text-sm text-primary hover:underline"
        >
          Blank check
        </button>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
        {flatSamples.map((sample) => (
          <button
            key={`${sample.checkType}-${sample.slug}`}
            type="button"
            onClick={() => onSelect(sample.checkType, sample)}
            className="flex items-start gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-accent hover:border-primary/30"
          >
            <FileText className="h-4 w-4 mt-0.5 text-muted-foreground shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 mb-1">
                <Badge variant="secondary" className="text-xs shrink-0">
                  {typeLabels[sample.checkType] || sample.checkType}
                </Badge>
                <span className="text-xs text-muted-foreground">
                  {formatPeriodLabel(sample.periodSeconds)}
                </span>
              </div>
              <div className="text-sm font-medium truncate">{sample.name}</div>
              <div className="text-xs text-muted-foreground truncate">
                {configSummary(sample.checkType, sample.config)}
              </div>
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}

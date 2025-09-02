import { useState } from "react";
import { ServiceLatendyChartPreview } from "./ui/service-latency-chart";
import {
  ServiceTile,
  ServiceTileExpanded,
  ServiceTileIcon,
  ServiceTilePing,
  ServiceTilePingDot,
  ServiceTilePreview,
  ServiceTileScope,
  ServiceTileSummary,
  ServiceTileTitle,
} from "./ui/service-tile";
import { StatusHoverCard } from "./ui/status-hover-card";
import {
  StatusSeries,
  StatusSeriesBar,
  StatusSeriesItem,
} from "./ui/status-series";
import { ThemeSwither } from "./ui/theme-switcher";
import {
  ServiceSummary,
  ServiceSummaryDescription,
  ServiceSummaryTitle,
} from "./ui/service-summary";

export function Playground_() {
  return (
    <div className="w-screen h-screen flex items-center justify-center">
      <div className="w-[500px] h-10">
        <StatusSeries>
          <StatusSeriesItem data-status="error">
            <StatusHoverCard
              title="No incident"
              description="Everything running smoothly"
            >
              <StatusSeriesBar />
            </StatusHoverCard>
          </StatusSeriesItem>
        </StatusSeries>
      </div>
    </div>
  );
}

export function Playground() {
  const [expanded, setExpanded] = useState<number | null>(0);
  return (
    <div className="w-screen h-screen flex items-center justify-center">
      <div className="w-[500px] flex flex-col gap-4">
        <Service
          name="Housing API"
          expanded={expanded === 0}
          onExpand={() => setExpanded(expanded === 0 ? null : 0)}
          status="ok"
          lastStatus="ok"
        />
        <Service
          name="Pricing API"
          expanded={expanded === 1}
          onExpand={() => setExpanded(expanded === 1 ? null : 1)}
          status="error"
          lastStatus="error"
        />
        <Service
          name="Booking API"
          expanded={expanded === 2}
          onExpand={() => setExpanded(expanded === 2 ? null : 2)}
          status="warning"
          lastStatus="warning"
        />
        <div className="fixed bottom-4 left-4">
          <ThemeSwither />
        </div>
      </div>
    </div>
  );
}

const RANDOM_STATUSES = Array.from({ length: 48 }, () =>
  Math.random() < 0.8
    ? "ok"
    : ["warning", "error"][Math.floor(Math.random() * 2)]
);

function Service({
  name,
  status,
  lastStatus,
  expanded,
  onExpand,
}: {
  name: string;
  status: string;
  lastStatus: string;
  expanded: boolean;
  onExpand: () => void;
}) {
  return (
    <ServiceTile data-status={status}>
      <ServiceTilePreview onClick={onExpand}>
        <ServiceTileTitle>
          <ServiceTileIcon />
          <ServiceTileScope>Public APIs</ServiceTileScope>
          {name}
        </ServiceTileTitle>
        <ServiceTileSummary>
          <StatusSeries>
            <StatusSeriesItem data-status="ok">
              <StatusSeriesBar />
            </StatusSeriesItem>
            <StatusSeriesItem data-status="ok">
              <StatusSeriesBar />
            </StatusSeriesItem>
            <StatusSeriesItem data-status="ok">
              <StatusSeriesBar />
            </StatusSeriesItem>
            <StatusSeriesItem data-status="ok">
              <StatusSeriesBar />
            </StatusSeriesItem>
            <StatusSeriesItem data-status={lastStatus}>
              <StatusSeriesBar />
            </StatusSeriesItem>
          </StatusSeries>
        </ServiceTileSummary>
        <ServiceTilePing
          title="Latency detected"
          description="Cloudfare maintenance is ongoing and causing latency on multiple services"
        >
          98.99% <ServiceTilePingDot />
        </ServiceTilePing>
      </ServiceTilePreview>
      <ServiceTileExpanded expanded={expanded}>
        <div className="pb-4 pt-6 border-t">
          <ServiceSummary>
            <ServiceSummaryTitle>All services operational</ServiceSummaryTitle>
            <ServiceSummaryDescription>
              Cloudfare maintenance is ongoing and causing latency on multiple
              services
            </ServiceSummaryDescription>
          </ServiceSummary>
          <ServiceLatendyChartPreview />
          <StatusSeries className="h-8 px-4">
            {RANDOM_STATUSES.map((randomStatus, i) => (
              <StatusSeriesItem
                key={i}
                data-status={i === 47 ? lastStatus : randomStatus}
              >
                <StatusSeriesBar />
              </StatusSeriesItem>
            ))}
          </StatusSeries>
        </div>
      </ServiceTileExpanded>
    </ServiceTile>
  );
}

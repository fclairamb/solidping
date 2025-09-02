"use client";

import { Area, AreaChart, CartesianGrid, XAxis } from "recharts";

import {
  ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart";

const mockedData = Array.from({ length: 144 }, (_, i) => {
  const hours = Math.floor((i * 5) / 60);
  const minutes = (i * 5) % 60;
  const time = `${hours.toString().padStart(2, "0")}:${minutes
    .toString()
    .padStart(2, "0")}`;
  let responseTimeMs = Math.floor(Math.random() * (210 - 170 + 1)) + 170;
  if (i >= 132) {
    responseTimeMs = Math.floor(Math.random() * (900 - 800 + 1)) + 200;
  }
  return { time, responseTimeMs };
});

const chartConfig = {
  responseTimeMs: {
    label: "Response Time (ms):",
    color: "var(--chart-2)",
  },
} satisfies ChartConfig;

export function ServiceLatendyChartPreview() {
  return (
    <div className="h-40 pt-4 px-0">
      <ChartContainer
        config={chartConfig}
        className="min-h-full h-full w-full px-0"
      >
        <AreaChart
          accessibilityLayer
          data={mockedData}
          margin={{
            left: 12,
            right: 12,
          }}
        >
          <CartesianGrid vertical={false} />
          <XAxis
            dataKey="month"
            tickLine={false}
            axisLine={false}
            tickMargin={8}
            tickFormatter={(value) => value.slice(0, 3)}
          />
          <ChartTooltip cursor={false} content={<ChartTooltipContent />} />
          <defs>
            <linearGradient id="responseTimeMs" x1="0" y1="0" x2="0" y2="1">
              <stop
                offset="5%"
                stopColor="var(--color-responseTimeMs)"
                stopOpacity={0.8}
              />
              <stop
                offset="95%"
                stopColor="var(--color-responseTimeMs)"
                stopOpacity={0.1}
              />
            </linearGradient>
          </defs>
          <Area
            dataKey="responseTimeMs"
            type="linear"
            fill="url(#responseTimeMs)"
            fillOpacity={0.4}
            stroke="var(--color-responseTimeMs)"
            stackId="a"
          />
        </AreaChart>
      </ChartContainer>
    </div>
  );
}

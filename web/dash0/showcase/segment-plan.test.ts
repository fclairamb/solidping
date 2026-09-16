import { describe, expect, it } from "vitest";
import {
  buildLabelWindows,
  buildOverlayPlan,
  buildSegmentPlan,
  buildSpeedFilters,
  buildSpeedTagWindows,
  mapSourceToOutput,
  type EditSpec,
  type PlanCue,
  type SegmentPlan,
} from "./segment-plan";

/** The cue list the committed choreography produces, times in seconds. */
const cues: PlanCue[] = [
  { t: 0.0, label: "login" },
  { t: 4.0, label: "rotation" },
  { t: 9.0, label: "checks-list" },
  { t: 12.0, label: "form-loaded" },
  { t: 20.0, label: "detail-page" },
  { t: 33.0, label: "chart" },
];

const timelapse: EditSpec = {
  kind: "speed",
  fromLabel: "detail-page",
  toLabel: "chart",
  speed: 3,
  minSpanS: 9,
  tag: "3×",
};

const bootstrapCut: EditSpec = {
  kind: "cut",
  fromLabel: "form-loaded",
  toLabel: "detail-page",
  minSpanS: 2,
};

describe("buildSegmentPlan", () => {
  it("is a single real-time segment when no time-lapse is asked for", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40 });

    expect(plan.segments).toEqual([{ start: 0, end: 40, speed: 1 }]);
    expect(plan.outputDuration).toBe(40);
    expect(plan.applied).toBe(0);
  });

  it("compresses the stretch between the two cues and keeps the rest real time", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });

    expect(plan.applied).toBe(1);
    expect(plan.segments).toEqual([
      { start: 0, end: 20, speed: 1 },
      { start: 20, end: 33, speed: 3, tag: "3×" },
      { start: 33, end: 40, speed: 1 },
    ]);
    // 20 + 13/3 + 7
    expect(plan.outputDuration).toBeCloseTo(31.333, 3);
  });

  it("only tags the segment it actually sped up", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });
    const tagged = plan.segments.filter((segment) => segment.tag);

    expect(tagged).toHaveLength(1);
    expect(tagged[0].speed).toBe(3);
    expect(
      plan.segments.every((segment) => segment.speed === 1 || segment.tag),
      "a sped-up segment must always carry a tag — an untagged speed-up lies",
    ).toBe(true);
  });

  it("declines, with a reason, when the gap is too short to be worth it", () => {
    const short: PlanCue[] = [
      { t: 20, label: "detail-page" },
      { t: 24, label: "chart" },
    ];
    const plan = buildSegmentPlan({
      cues: short,
      sourceDuration: 30,
      edits: [timelapse],
    });

    expect(plan.applied).toBe(0);
    expect(plan.notes.join(" ")).toContain("4.00s");
    expect(plan.segments).toEqual([{ start: 0, end: 30, speed: 1 }]);
  });

  it("declines, with a reason, when a cue is missing rather than throwing", () => {
    const plan = buildSegmentPlan({
      cues: [{ t: 20, label: "detail-page" }],
      sourceDuration: 40,
      edits: [timelapse],
    });

    expect(plan.applied).toBe(0);
    expect(plan.notes.join(" ")).toContain("chart");
  });

  it("clamps a cue that sits past the end of the trimmed take", () => {
    const plan = buildSegmentPlan({
      cues: [
        { t: 20, label: "detail-page" },
        { t: 99, label: "chart" },
      ],
      sourceDuration: 40,
      edits: [timelapse],
    });

    expect(plan.segments[plan.segments.length - 1].end).toBe(40);
    expect(
      plan.segments.every((segment) => segment.end <= 40),
      "no segment may reference source time the take does not have",
    ).toBe(true);
  });

  it("covers the source exactly once, with no gap and no overlap", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });

    expect(plan.segments[0].start).toBe(0);
    expect(plan.segments[plan.segments.length - 1].end).toBe(40);
    for (let i = 1; i < plan.segments.length; i++) {
      expect(plan.segments[i].start).toBe(plan.segments[i - 1].end);
    }
  });

  it("returns an empty plan for an empty take", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 0, edits: [timelapse] });

    expect(plan.segments).toEqual([]);
    expect(plan.outputDuration).toBe(0);
  });
});

describe("mapSourceToOutput", () => {
  const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });

  it("is the identity before the compressed stretch", () => {
    expect(mapSourceToOutput(plan, 0)).toBe(0);
    expect(mapSourceToOutput(plan, 12)).toBe(12);
    expect(mapSourceToOutput(plan, 20)).toBe(20);
  });

  it("compresses inside the stretch", () => {
    expect(mapSourceToOutput(plan, 26.5)).toBeCloseTo(20 + 6.5 / 3, 6);
    expect(mapSourceToOutput(plan, 33)).toBeCloseTo(20 + 13 / 3, 6);
  });

  it("stays shifted after it", () => {
    expect(mapSourceToOutput(plan, 40)).toBeCloseTo(plan.outputDuration, 6);
  });

  it("is monotonic", () => {
    let last = -1;
    for (let t = 0; t <= 40; t += 0.25) {
      const mapped = mapSourceToOutput(plan, t);
      expect(mapped).toBeGreaterThanOrEqual(last);
      last = mapped;
    }
  });

  it("never exceeds the output duration", () => {
    expect(mapSourceToOutput(plan, 1000)).toBeCloseTo(plan.outputDuration, 6);
  });
});

describe("buildLabelWindows", () => {
  const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });
  const specs = [
    { atCue: null, text: "Run it", holdS: 3 },
    { atCue: "login", text: "First login", holdS: 3 },
    { atCue: "checks-list", text: "First check", holdS: 3 },
    { atCue: "detail-page", text: "Results from two regions", holdS: 3 },
  ];

  const windows = buildLabelWindows({
    plan,
    cues,
    labels: specs,
    offsetS: 7,
    outputDuration: 7 + plan.outputDuration,
  });

  it("puts every label on the timeline exactly once", () => {
    expect(windows.map((w) => w.text)).toEqual([
      "Run it",
      "First login",
      "First check",
      "Results from two regions",
    ]);
    expect(new Set(windows.map((w) => w.text)).size).toBe(windows.length);
  });

  it("anchors each label to its cue, offset by the terminal segment", () => {
    expect(windows[0].start).toBe(0);
    expect(windows[1].start).toBe(7);
    expect(windows[2].start).toBe(16);
    expect(windows[3].start).toBe(27);
  });

  it("never shows two labels at once", () => {
    for (let i = 1; i < windows.length; i++) {
      expect(windows[i].start).toBeGreaterThanOrEqual(windows[i - 1].end);
    }
  });

  it("truncates a label that a later one runs into", () => {
    const tight = buildLabelWindows({
      plan,
      cues,
      labels: [
        { atCue: null, text: "Run it", holdS: 30 },
        { atCue: "login", text: "First login", holdS: 3 },
      ],
      offsetS: 7,
      outputDuration: 7 + plan.outputDuration,
    });

    expect(tight[0].end).toBe(7);
    expect(tight[1].start).toBe(7);
  });

  it("clamps the last window to the end of the cut", () => {
    const total = 7 + plan.outputDuration;
    const late = buildLabelWindows({
      plan,
      cues,
      labels: [{ atCue: "chart", text: "Results", holdS: 60 }],
      offsetS: 7,
      outputDuration: total,
    });

    expect(late[0].end).toBe(total);
  });

  it("throws when a label's cue is not in the take", () => {
    expect(() =>
      buildLabelWindows({
        plan,
        cues,
        labels: [{ atCue: "regions", text: "Regions", holdS: 3 }],
        offsetS: 7,
        outputDuration: 40,
      }),
    ).toThrow(/regions/);
  });
});

describe("buildSpeedTagWindows", () => {
  it("covers exactly the sped-up stretch on the output timeline", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });
    const tags = buildSpeedTagWindows(plan, 7);

    expect(tags).toHaveLength(1);
    expect(tags[0].text).toBe("3×");
    expect(tags[0].start).toBeCloseTo(27, 6);
    expect(tags[0].end).toBeCloseTo(27 + 13 / 3, 6);
  });

  it("is empty when nothing was compressed", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40 });

    expect(buildSpeedTagWindows(plan, 7)).toEqual([]);
  });
});

describe("buildSpeedFilters", () => {
  it("is null when every segment plays in real time", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40 });

    expect(buildSpeedFilters(plan, { inLabel: "b", outLabel: "bv", fps: 25 })).toBeNull();
  });

  it("splits, retimes and re-concatenates every segment", () => {
    const plan = buildSegmentPlan({ cues, sourceDuration: 40, edits: [timelapse] });
    const filters = buildSpeedFilters(plan, {
      inLabel: "b",
      outLabel: "bv",
      fps: 25,
    });

    expect(filters).not.toBeNull();
    const graph = (filters as string[]).join(";");
    expect(graph).toContain("[b]split=3");
    expect(graph).toContain("trim=20:33,setpts=(PTS-STARTPTS)/3");
    expect(graph).toContain("concat=n=3:v=1:a=0[bv]");
    // Every stream the chain declares is consumed again by the concat.
    for (let i = 0; i < 3; i++) {
      expect(graph).toContain(`[sp${i}]`);
    }
  });
});

describe("buildOverlayPlan", () => {
  const windows = [
    { text: "Run it", start: 0.5, end: 3.5, file: "a.png", x: "56", y: "H-h-64" },
    { text: "First login", start: 7, end: 10, file: "b.png", x: "56", y: "H-h-64" },
  ];

  it("declares one looped input per caption, for exactly its own duration", () => {
    const plan = buildOverlayPlan(windows, {
      firstInput: 2,
      baseLabel: "base",
      outLabel: "out",
      fadeS: 0.25,
      fps: 25,
    });

    expect(plan.inputs).toEqual([
      { file: "a.png", durationS: 3 },
      { file: "b.png", durationS: 3 },
    ]);
  });

  it("chains the overlays and lands on the requested output label", () => {
    const plan = buildOverlayPlan(windows, {
      firstInput: 2,
      baseLabel: "base",
      outLabel: "out",
      fadeS: 0.25,
      fps: 25,
    });
    const graph = plan.filters.join(";");

    expect(graph).toContain("[2:v]format=rgba");
    expect(graph).toContain("[3:v]format=rgba");
    expect(graph).toContain("[base][lbl0]overlay=56:H-h-64:enable='between(t,0.5,3.5)'[ov0]");
    expect(graph).toContain("[ov0][lbl1]overlay=");
    expect(plan.filters[plan.filters.length - 1]).toContain("[out]");
  });

  it("shifts each still to its own window and fades within it", () => {
    const plan = buildOverlayPlan(windows, {
      firstInput: 2,
      baseLabel: "base",
      outLabel: "out",
      fadeS: 0.25,
      fps: 25,
    });

    expect(plan.filters[0]).toContain("setpts=PTS+0.5/TB");
    expect(plan.filters[0]).toContain("fade=t=in:st=0:d=0.25:alpha=1");
    expect(plan.filters[0]).toContain("fade=t=out:st=2.75:d=0.25:alpha=1");
  });

  it("never fades longer than half the window it has", () => {
    const plan = buildOverlayPlan(
      [{ text: "x", start: 1, end: 1.2, file: "x.png", x: "0", y: "0" }],
      { firstInput: 2, baseLabel: "base", outLabel: "out", fadeS: 0.25, fps: 25 },
    );

    expect(plan.filters[0]).toContain("fade=t=in:st=0:d=0.1:alpha=1");
    expect(plan.filters[0]).toContain("fade=t=out:st=0.1:d=0.1:alpha=1");
  });

  it("passes the base through untouched when there is nothing to burn in", () => {
    const plan = buildOverlayPlan([], {
      firstInput: 2,
      baseLabel: "base",
      outLabel: "out",
      fadeS: 0.25,
      fps: 25,
    });

    expect(plan.inputs).toEqual([]);
    expect(plan.filters).toEqual(["[base]null[out]"]);
  });
});

describe("the plan a short-interval take produces", () => {
  it("publishes real time end to end when the dwell is already brief", () => {
    // A 5-second check interval needs a ~7 s dwell, not the 11 s a 10-second
    // interval forces — and the spec prefers no time-lapse when that is true.
    const brief: PlanCue[] = [
      { t: 20, label: "detail-page" },
      { t: 27, label: "chart" },
    ];
    const plan: SegmentPlan = buildSegmentPlan({
      cues: brief,
      sourceDuration: 34,
      edits: [timelapse],
    });

    expect(plan.applied).toBe(0);
    expect(plan.outputDuration).toBe(34);
    expect(buildSpeedTagWindows(plan, 7)).toEqual([]);
  });
});

describe("a cut edit", () => {
  it("removes the stretch entirely, with no tag to explain it", () => {
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [bootstrapCut],
    });

    expect(plan.applied).toBe(1);
    expect(plan.segments).toEqual([
      { start: 0, end: 12, speed: 1 },
      { start: 20, end: 40, speed: 1 },
    ]);
    expect(plan.outputDuration).toBe(32);
    expect(plan.segments.some((segment) => segment.tag)).toBe(false);
  });

  it("joins the two sides at one instant on the output timeline", () => {
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [bootstrapCut],
    });

    // Everything inside the removed stretch lands on the join.
    expect(mapSourceToOutput(plan, 12)).toBe(12);
    expect(mapSourceToOutput(plan, 16)).toBe(12);
    expect(mapSourceToOutput(plan, 20)).toBe(12);
    expect(mapSourceToOutput(plan, 21)).toBe(13);
  });

  it("combines with a speed edit elsewhere in the take", () => {
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [bootstrapCut, timelapse],
    });

    expect(plan.applied).toBe(2);
    expect(plan.segments).toEqual([
      { start: 0, end: 12, speed: 1 },
      { start: 20, end: 33, speed: 3, tag: "3×" },
      { start: 33, end: 40, speed: 1 },
    ]);
    // 12 + 13/3 + 7
    expect(plan.outputDuration).toBeCloseTo(23.333, 3);
  });

  it("orders edits by where they fall, not by how they were listed", () => {
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [timelapse, bootstrapCut],
    });

    expect(plan.segments[0]).toEqual({ start: 0, end: 12, speed: 1 });
    expect(plan.segments[1].speed).toBe(3);
  });

  it("refuses the second of two edits over the same footage", () => {
    const overlapping: EditSpec = {
      kind: "cut",
      fromLabel: "checks-list",
      toLabel: "detail-page",
      minSpanS: 1,
    };
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [bootstrapCut, overlapping],
    });

    expect(plan.applied).toBe(1);
    expect(plan.notes.join(" ")).toContain("overlaps");
  });

  it("captions after a cut sit where the footage actually landed", () => {
    const plan = buildSegmentPlan({
      cues,
      sourceDuration: 40,
      edits: [bootstrapCut],
    });
    const windows = buildLabelWindows({
      plan,
      cues,
      labels: [{ atCue: "detail-page", text: "Results", holdS: 3 }],
      offsetS: 7,
      outputDuration: 7 + plan.outputDuration,
    });

    // detail-page is at source t=20, but 8 s were cut out before it.
    expect(windows[0].start).toBe(19);
  });
});

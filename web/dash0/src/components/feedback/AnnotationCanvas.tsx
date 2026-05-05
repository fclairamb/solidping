import { useEffect, useRef, useState } from "react";

import type { Annotation } from "./types";
import type { AnnotationTool } from "./AnnotationToolbar";

interface AnnotationCanvasProps {
  imageURL: string;
  tool: AnnotationTool;
  annotations: Annotation[];
  onAnnotationsChange: (annotations: Annotation[]) => void;
}

const ANNOTATION_COLOR = "#ef4444";
const STROKE_WIDTH = 3;

// AnnotationCanvas overlays drawing tools on top of a screenshot. Coordinates
// are normalized to [0, 1] so annotations stay correct if the displayed image
// is resized between draw and submit. The actual rasterization for submission
// happens via renderAnnotations on a backing canvas.
export function AnnotationCanvas({
  imageURL,
  tool,
  annotations,
  onAnnotationsChange,
}: AnnotationCanvasProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const overlayRef = useRef<HTMLCanvasElement>(null);
  const [drawing, setDrawing] = useState<Annotation | null>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });

  useEffect(() => {
    if (!containerRef.current) return;
    const update = () => {
      const rect = containerRef.current?.getBoundingClientRect();
      if (rect) setSize({ w: rect.width, h: rect.height });
    };
    update();
    const ro = new ResizeObserver(update);
    ro.observe(containerRef.current);

    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const canvas = overlayRef.current;
    if (!canvas || size.w === 0) return;
    canvas.width = size.w;
    canvas.height = size.h;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    renderAnnotations(ctx, [...annotations, ...(drawing ? [drawing] : [])], {
      width: size.w,
      height: size.h,
    });
  }, [annotations, drawing, size]);

  function relativeFromEvent(event: React.PointerEvent<HTMLCanvasElement>) {
    const rect = event.currentTarget.getBoundingClientRect();
    return {
      x: (event.clientX - rect.left) / rect.width,
      y: (event.clientY - rect.top) / rect.height,
    };
  }

  function startDraw(event: React.PointerEvent<HTMLCanvasElement>) {
    if (tool === "select") return;
    const { x, y } = relativeFromEvent(event);

    if (tool === "rect") {
      setDrawing({ type: "rect", x, y, w: 0, h: 0, color: ANNOTATION_COLOR });
    } else if (tool === "arrow") {
      setDrawing({
        type: "arrow",
        x,
        y,
        x2: x,
        y2: y,
        color: ANNOTATION_COLOR,
      });
    } else if (tool === "text") {
      const text = window.prompt("Annotation text") || "";
      if (text) {
        onAnnotationsChange([
          ...annotations,
          { type: "text", x, y, text, color: ANNOTATION_COLOR },
        ]);
      }
    }
  }

  function continueDraw(event: React.PointerEvent<HTMLCanvasElement>) {
    if (!drawing) return;
    const { x, y } = relativeFromEvent(event);
    if (drawing.type === "rect") {
      setDrawing({ ...drawing, w: x - drawing.x, h: y - drawing.y });
    } else if (drawing.type === "arrow") {
      setDrawing({ ...drawing, x2: x, y2: y });
    }
  }

  function endDraw() {
    if (!drawing) return;
    onAnnotationsChange([...annotations, drawing]);
    setDrawing(null);
  }

  return (
    <div
      ref={containerRef}
      className="relative inline-block"
      data-testid="annotation-container"
    >
      <img
        src={imageURL}
        alt="Screenshot"
        className="block max-h-64 w-auto rounded border"
        draggable={false}
      />
      <canvas
        ref={overlayRef}
        className="absolute inset-0 h-full w-full"
        style={{ cursor: tool === "select" ? "default" : "crosshair" }}
        onPointerDown={startDraw}
        onPointerMove={continueDraw}
        onPointerUp={endDraw}
        onPointerLeave={endDraw}
        data-testid="annotation-canvas"
      />
    </div>
  );
}

interface CanvasSize {
  width: number;
  height: number;
}

// renderAnnotations is exported as a pure function so it can be unit-tested
// against node-canvas, and reused at submit time to bake the overlay into
// the outgoing PNG.
export function renderAnnotations(
  ctx: CanvasRenderingContext2D,
  annotations: Annotation[],
  size: CanvasSize,
): void {
  ctx.clearRect(0, 0, size.width, size.height);
  ctx.lineWidth = STROKE_WIDTH;
  ctx.font = "16px sans-serif";

  for (const ann of annotations) {
    ctx.strokeStyle = ann.color;
    ctx.fillStyle = ann.color;

    if (ann.type === "rect") {
      ctx.strokeRect(
        ann.x * size.width,
        ann.y * size.height,
        ann.w * size.width,
        ann.h * size.height,
      );
    } else if (ann.type === "arrow") {
      drawArrow(
        ctx,
        ann.x * size.width,
        ann.y * size.height,
        ann.x2 * size.width,
        ann.y2 * size.height,
      );
    } else if (ann.type === "text") {
      ctx.fillText(ann.text, ann.x * size.width, ann.y * size.height);
    }
  }
}

function drawArrow(
  ctx: CanvasRenderingContext2D,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
): void {
  const headLen = 10;
  const angle = Math.atan2(y2 - y1, x2 - x1);

  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();

  ctx.beginPath();
  ctx.moveTo(x2, y2);
  ctx.lineTo(
    x2 - headLen * Math.cos(angle - Math.PI / 6),
    y2 - headLen * Math.sin(angle - Math.PI / 6),
  );
  ctx.lineTo(
    x2 - headLen * Math.cos(angle + Math.PI / 6),
    y2 - headLen * Math.sin(angle + Math.PI / 6),
  );
  ctx.closePath();
  ctx.fill();
}

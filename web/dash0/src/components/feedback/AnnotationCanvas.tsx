import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { Annotation } from "./types";
import type { AnnotationTool } from "./AnnotationToolbar";
import {
  boundsOf,
  normalizeRect,
  pixelate,
  topmostHit,
  translateAnnotation,
  type Size,
} from "./annotationGeometry";

interface AnnotationCanvasProps {
  imageURL: string;
  tool: AnnotationTool;
  color: string;
  annotations: Annotation[];
  onAnnotationsChange: (annotations: Annotation[]) => void;
}

const STROKE_WIDTH = 3;
const HIGHLIGHT_ALPHA = 0.35;

interface TextDraft {
  x: number;
  y: number;
  value: string;
}

interface DragState {
  index: number;
  lastX: number;
  lastY: number;
  moved: boolean;
}

// AnnotationCanvas overlays drawing tools on top of a screenshot. Coordinates
// are normalized to [0, 1] so annotations stay correct if the displayed image
// is resized between draw and submit. The actual rasterization for submission
// happens via renderAnnotations on a backing canvas.
export function AnnotationCanvas({
  imageURL,
  tool,
  color,
  annotations,
  onAnnotationsChange,
}: AnnotationCanvasProps) {
  const { t } = useTranslation("feedback");
  const containerRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  const overlayRef = useRef<HTMLCanvasElement>(null);
  const [drawing, setDrawing] = useState<Annotation | null>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [loaded, setLoaded] = useState(false);
  const [selectedRaw, setSelected] = useState<number | null>(null);
  const [textDraft, setTextDraft] = useState<TextDraft | null>(null);
  const dragRef = useRef<DragState | null>(null);
  const textDone = useRef(false);

  useEffect(() => {
    if (!containerRef.current) return undefined;
    const update = () => {
      const rect = containerRef.current?.getBoundingClientRect();
      if (rect) setSize({ w: rect.width, h: rect.height });
    };
    update();
    const ro = new ResizeObserver(update);
    ro.observe(containerRef.current);

    return () => ro.disconnect();
  }, []);

  // A removed annotation (undo, delete) or another tool must not leave a
  // dangling selection.
  const selected =
    tool === "select" && selectedRaw !== null && selectedRaw < annotations.length
      ? selectedRaw
      : null;

  useEffect(() => {
    const canvas = overlayRef.current;
    const img = imgRef.current;
    if (!canvas || size.w === 0 || !loaded || !img) return;
    canvas.width = size.w;
    canvas.height = size.h;
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    if (!ctx) return;
    const dims = { width: size.w, height: size.h };
    renderAnnotations(ctx, [...annotations, ...(drawing ? [drawing] : [])], dims, img);

    if (selected !== null && annotations[selected]) {
      const b = boundsOf(annotations[selected], dims);
      ctx.save();
      ctx.strokeStyle = "#2563eb";
      ctx.lineWidth = 1.5;
      ctx.setLineDash([5, 3]);
      ctx.strokeRect(b.x - 4, b.y - 4, b.w + 8, b.h + 8);
      ctx.restore();
    }
  }, [annotations, drawing, size, loaded, selected]);

  function relativeFromEvent(event: React.PointerEvent<HTMLCanvasElement>) {
    const rect = event.currentTarget.getBoundingClientRect();
    return {
      x: (event.clientX - rect.left) / rect.width,
      y: (event.clientY - rect.top) / rect.height,
    };
  }

  function startDraw(event: React.PointerEvent<HTMLCanvasElement>) {
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const { x, y } = relativeFromEvent(event);

    if (tool === "select") {
      const hit = topmostHit(annotations, x, y, { width: size.w, height: size.h });
      setSelected(hit >= 0 ? hit : null);
      dragRef.current = hit >= 0 ? { index: hit, lastX: x, lastY: y, moved: false } : null;
      return;
    }

    if (tool === "rect" || tool === "highlight" || tool === "blur") {
      setDrawing({ type: tool, x, y, w: 0, h: 0, color });
    } else if (tool === "arrow") {
      setDrawing({ type: "arrow", x, y, x2: x, y2: y, color });
    } else if (tool === "pen") {
      setDrawing({ type: "pen", x, y, points: [{ x, y }], color });
    }
    // The text tool opens its input on pointer up, once the browser is done
    // moving focus around for this gesture.
  }

  function continueDraw(event: React.PointerEvent<HTMLCanvasElement>) {
    const { x, y } = relativeFromEvent(event);

    const drag = dragRef.current;
    if (drag) {
      const dx = x - drag.lastX;
      const dy = y - drag.lastY;
      drag.lastX = x;
      drag.lastY = y;
      drag.moved = true;
      onAnnotationsChange(
        annotations.map((a, i) => (i === drag.index ? translateAnnotation(a, dx, dy) : a)),
      );
      return;
    }

    if (!drawing) return;
    if (drawing.type === "rect" || drawing.type === "highlight" || drawing.type === "blur") {
      setDrawing({ ...drawing, w: x - drawing.x, h: y - drawing.y });
    } else if (drawing.type === "arrow") {
      setDrawing({ ...drawing, x2: x, y2: y });
    } else if (drawing.type === "pen") {
      setDrawing({ ...drawing, points: [...drawing.points, { x, y }] });
    }
  }

  function endDraw(event: React.PointerEvent<HTMLCanvasElement>) {
    dragRef.current = null;

    if (tool === "text" && !textDraft) {
      const { x, y } = relativeFromEvent(event);
      textDone.current = false;
      setTextDraft({ x, y, value: "" });
      return;
    }
    if (!drawing) return;
    onAnnotationsChange([...annotations, drawing]);
    setDrawing(null);
  }

  function commitText() {
    if (textDone.current || !textDraft) return;
    textDone.current = true;
    const value = textDraft.value.trim();
    if (value) {
      onAnnotationsChange([
        ...annotations,
        { type: "text", x: textDraft.x, y: textDraft.y, text: value, color },
      ]);
    }
    setTextDraft(null);
  }

  function cancelText() {
    textDone.current = true;
    setTextDraft(null);
  }

  function onKeyDown(event: React.KeyboardEvent<HTMLCanvasElement>) {
    if ((event.key === "Delete" || event.key === "Backspace") && selected !== null) {
      event.preventDefault();
      onAnnotationsChange(annotations.filter((_, i) => i !== selected));
      setSelected(null);
    }
  }

  return (
    <div
      ref={containerRef}
      className="relative inline-block max-w-full"
      data-testid="annotation-container"
    >
      <img
        ref={imgRef}
        src={imageURL}
        alt={t("screenshot_alt")}
        className="block max-h-64 w-auto max-w-full rounded border"
        draggable={false}
        onLoad={() => setLoaded(true)}
      />
      <canvas
        ref={overlayRef}
        tabIndex={0}
        className="absolute inset-0 h-full w-full outline-none"
        style={{
          cursor: tool === "select" ? "default" : "crosshair",
          touchAction: "none",
        }}
        onPointerDown={startDraw}
        onPointerMove={continueDraw}
        onPointerUp={endDraw}
        onPointerCancel={endDraw}
        onKeyDown={onKeyDown}
        data-testid="annotation-canvas"
      />
      {textDraft && (
        <input
          autoFocus
          type="text"
          value={textDraft.value}
          placeholder={t("text_placeholder")}
          aria-label={t("text_placeholder")}
          data-testid="annotation-text-input"
          className="absolute z-10 h-8 w-40 rounded border bg-background px-2 text-base"
          style={{
            left: `${Math.min(textDraft.x * 100, 70)}%`,
            top: `${textDraft.y * 100}%`,
            transform: "translateY(-100%)",
          }}
          onChange={(e) => setTextDraft({ ...textDraft, value: e.target.value })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commitText();
            } else if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              cancelText();
            }
          }}
          onBlur={commitText}
        />
      )}
    </div>
  );
}

// renderAnnotations is exported so it can be reused at submit time to bake the
// overlay into the outgoing PNG. When `base` is given it is drawn first
// (scaled to `size`), which is also what the blur tool pixelates.
export function renderAnnotations(
  ctx: CanvasRenderingContext2D,
  annotations: Annotation[],
  size: Size,
  base?: CanvasImageSource,
): void {
  ctx.clearRect(0, 0, size.width, size.height);
  if (base) ctx.drawImage(base, 0, 0, size.width, size.height);
  ctx.lineWidth = STROKE_WIDTH;
  ctx.lineJoin = "round";
  ctx.lineCap = "round";
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
    } else if (ann.type === "highlight") {
      ctx.save();
      ctx.globalAlpha = HIGHLIGHT_ALPHA;
      ctx.fillRect(
        ann.x * size.width,
        ann.y * size.height,
        ann.w * size.width,
        ann.h * size.height,
      );
      ctx.restore();
    } else if (ann.type === "blur") {
      blurRegion(ctx, boundsOf(ann, size), size);
    } else if (ann.type === "arrow") {
      drawArrow(
        ctx,
        ann.x * size.width,
        ann.y * size.height,
        ann.x2 * size.width,
        ann.y2 * size.height,
      );
    } else if (ann.type === "pen") {
      ctx.beginPath();
      ann.points.forEach((p, i) => {
        const px = p.x * size.width;
        const py = p.y * size.height;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
      });
      if (ann.points.length === 1) {
        ctx.lineTo(ann.points[0].x * size.width + 0.1, ann.points[0].y * size.height);
      }
      ctx.stroke();
    } else if (ann.type === "text") {
      ctx.fillText(ann.text, ann.x * size.width, ann.y * size.height);
    }
  }
}

function blurRegion(
  ctx: CanvasRenderingContext2D,
  region: { x: number; y: number; w: number; h: number },
  size: Size,
): void {
  const x = Math.max(0, Math.floor(region.x));
  const y = Math.max(0, Math.floor(region.y));
  const w = Math.min(Math.ceil(size.width), Math.ceil(region.x + region.w)) - x;
  const h = Math.min(Math.ceil(size.height), Math.ceil(region.y + region.h)) - y;
  if (w <= 0 || h <= 0) return;

  const rect = normalizeRect({ x: 0, y: 0, w, h });
  const image = ctx.getImageData(x, y, w, h);
  pixelate(image, rect, Math.max(6, Math.round(size.width / 60)));
  ctx.putImageData(image, x, y);
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

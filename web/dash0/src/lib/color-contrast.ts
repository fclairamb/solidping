// Pure color math for the contrast unit tests (spec 2026-09-24-02). Nothing
// in the app imports this at runtime: it exists so a test can compute what the
// browser actually paints — an oklch() token clipped to sRGB, a point along a
// gradient, a translucent text color composited over it — and hold the WCAG
// ratio to a threshold. theme-tokens.test.ts carries the same conversion.

export type Oklch = [number, number, number];
type Oklab = [number, number, number];
type Rgb = [number, number, number];

/** Accepts both lightness spellings: index.css writes `0.55`, Tailwind's own
 * palette `55%`. */
export function parseOklch(value: string): Oklch {
  const m = /oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+)/.exec(value);
  if (!m) throw new Error(`not an oklch() color: ${value}`);
  const lightness = Number(m[1]) / (m[2] === "%" ? 100 : 1);
  return [lightness, Number(m[3]), Number(m[4])];
}

/** The color stops of a linear-gradient() written with oklch() colors, with
 * their positions (0..1). Positionless stops are spread evenly between their
 * positioned neighbours, as CSS does. */
export function gradientStops(value: string): { color: Oklch; at: number }[] {
  const raw = [...value.matchAll(/(oklch\([^)]*\))(?:\s+([\d.]+)%)?/g)].map((m) => ({
    color: parseOklch(m[1]),
    at: m[2] === undefined ? undefined : Number(m[2]) / 100,
  }));
  if (raw.length < 2) throw new Error(`not a gradient: ${value}`);
  if (raw[0].at === undefined) raw[0].at = 0;
  if (raw[raw.length - 1].at === undefined) raw[raw.length - 1].at = 1;
  for (let i = 1; i < raw.length - 1; i++) {
    if (raw[i].at !== undefined) continue;
    let j = i;
    while (raw[j].at === undefined) j++;
    const from = raw[i - 1].at as number;
    const to = raw[j].at as number;
    for (let k = i; k < j; k++) raw[k].at = from + ((to - from) * (k - i + 1)) / (j - i + 1);
  }
  return raw.map((s) => ({ color: s.color, at: s.at as number }));
}

function toOklab([L, C, H]: Oklch): Oklab {
  const h = (H * Math.PI) / 180;
  return [L, C * Math.cos(h), C * Math.sin(h)];
}

/** oklab → gamma-encoded sRGB, out-of-gamut channels clipped and quantized to
 * 8 bits, as the painted pixel is. */
function oklabToSrgb([L, a, b]: Oklab): Rgb {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const linear = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  return linear.map((v) => {
    const c = Math.min(1, Math.max(0, v));
    const encoded = c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
    return Math.round(Math.min(1, Math.max(0, encoded)) * 255) / 255;
  }) as Rgb;
}

export function oklchToSrgb(color: Oklch): Rgb {
  return oklabToSrgb(toOklab(color));
}

/** The painted color at position t (0..1) along a gradient. oklch() stops
 * make it a non-legacy gradient, which CSS interpolates in Oklab. */
export function gradientColorAt(stops: { color: Oklch; at: number }[], t: number): Rgb {
  const clamped = Math.min(1, Math.max(0, t));
  for (let i = 0; i < stops.length - 1; i++) {
    const a = stops[i];
    const b = stops[i + 1];
    if (clamped <= b.at) {
      const u = b.at === a.at ? 0 : (clamped - a.at) / (b.at - a.at);
      const la = toOklab(a.color);
      const lb = toOklab(b.color);
      return oklabToSrgb(la.map((v, k) => v + (lb[k] - v) * u) as Oklab);
    }
  }
  return oklchToSrgb(stops[stops.length - 1].color);
}

/** A translucent foreground composited over an opaque background, in encoded
 * sRGB (how browsers blend), then quantized. */
export function composite(fg: Rgb, alpha: number, bg: Rgb): Rgb {
  return fg.map((v, i) => Math.round((alpha * v + (1 - alpha) * bg[i]) * 255) / 255) as Rgb;
}

function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrastRatio(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

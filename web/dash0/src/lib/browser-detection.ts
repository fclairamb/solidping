/** Derives a short human-readable device label from navigator.userAgent. */
export function deriveDeviceLabel(): string {
  const ua = navigator.userAgent;
  let browser = "Browser";
  let os = "Unknown OS";

  if (/Chrome\/[\d.]+/.test(ua) && !/Edg\//.test(ua) && !/OPR\//.test(ua)) {
    browser = "Chrome";
  } else if (/Firefox\//.test(ua)) {
    browser = "Firefox";
  } else if (/Edg\//.test(ua)) {
    browser = "Edge";
  } else if (/OPR\/|Opera\//.test(ua)) {
    browser = "Opera";
  } else if (/Safari\//.test(ua) && !/Chrome\//.test(ua)) {
    browser = "Safari";
  }

  if (/Windows/.test(ua)) {
    os = "Windows";
  } else if (/Macintosh|Mac OS X/.test(ua)) {
    os = "macOS";
  } else if (/Linux/.test(ua) && !/Android/.test(ua)) {
    os = "Linux";
  } else if (/Android/.test(ua)) {
    os = "Android";
  } else if (/iPhone|iPad/.test(ua)) {
    os = "iOS";
  }

  return `${browser} on ${os}`;
}

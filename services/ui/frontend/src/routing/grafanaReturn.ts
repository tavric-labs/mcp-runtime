import { grafanaPath } from "../api/config";

// Return links are untrusted, including hand-edited sign-in URLs. Limit full
// page navigation to this deployment's Grafana pages on the current origin.
export function grafanaReturnPath(value: string | undefined): string {
  if (!value?.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) {
    return "";
  }
  try {
    const target = new URL(value, window.location.origin);
    const path = decodeURIComponent(target.pathname);
    const base = grafanaPath();
    if (target.origin !== window.location.origin || /[\\\u0000-\u001f\u007f]/.test(path)) return "";
    if (path !== base && !path.startsWith(`${base}/`)) return "";
    if (path === `${base}/api` || path.startsWith(`${base}/api/`)) return "";
    return target.pathname + target.search + target.hash;
  } catch {
    return "";
  }
}

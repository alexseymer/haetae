import { isValidDomain } from "./deploy";

export const PROJECT_NAME_RE = /^[a-z0-9][a-z0-9-]{0,39}$/;
export const GIT_REF_RE =
  /^(?:[0-9a-f]{40}|[A-Za-z0-9][A-Za-z0-9._/-]{0,127})$/;
export const MAX_INLINE_COMPOSE = 256 * 1024;

export function validateProjectName(name: string): string | null {
  if (!PROJECT_NAME_RE.test(name)) {
    return "Use 1–40 characters: lowercase letters, digits and dashes, starting with a letter or digit";
  }
  return null;
}

export function validateGitUrl(url: string): string | null {
  if (!url) return "Enter the repository URL, for example https://github.com/acme/shop";
  if (!url.startsWith("https://")) return "Repository URL must start with https:// (ssh:// and git@ addresses are not supported)";
  try {
    const u = new URL(url);
    if (u.protocol !== "https:" || !u.host || u.username || u.password) {
      return "Repository URL must be https://host/path without credentials";
    }
  } catch {
    return "Repository URL is not valid";
  }
  return null;
}

export function validateGitRef(ref: string): string | null {
  if (!ref) return "Enter a branch name such as main, or a 40-character commit id";
  if (/^[0-9a-f]{40}$/.test(ref)) return null;
  if (
    !GIT_REF_RE.test(ref) ||
    ref.includes("..") ||
    ref.includes("//") ||
    ref.endsWith("/") ||
    ref.endsWith(".") ||
    ref.endsWith(".lock") ||
    ref.includes("@{")
  ) {
    return "Ref must be a branch name or a 40-character commit id";
  }
  return null;
}

export function validateIngress(
  domain: string,
  service: string,
  port: string,
): string | null {
  if (!domain && !service && !port) return null;
  if (domain || service || port) {
    if (!domain || !service || !port) {
      return "Domain, service and port are required together";
    }
  }
  if (domain && !isValidDomain(domain)) {
    return "Domain must be a lower-case host name such as app.example.com";
  }
  const n = Number(port);
  if (!Number.isInteger(n) || n < 1 || n > 65535) {
    return "Port must be between 1 and 65535";
  }
  return null;
}

export function validateInlineCompose(text: string): string | null {
  if (!text.trim()) return "Paste your compose file, or switch the source to a Git repository";
  if (new TextEncoder().encode(text).length > MAX_INLINE_COMPOSE) {
    return "Compose file is larger than 256 KiB. Shorten it or use a Git repository instead";
  }
  return null;
}

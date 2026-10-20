import { rules } from "@/api/rules";

const DURATION = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;
const SECRET_NAME = new RegExp(rules.secretNamePattern);
const WORKSPACE_SLUG = new RegExp(rules.workspaceSlugPattern);

export const isDuration = (v: string) => DURATION.test(v.trim());
export const isSecretName = (v: string) => SECRET_NAME.test(v);
export const isWorkspaceSlug = (v: string) => WORKSPACE_SLUG.test(v);

export function isHttpUrl(v: string): boolean {
  try {
    const u = new URL(v.trim());
    return (u.protocol === "http:" || u.protocol === "https:") && u.hostname !== "";
  } catch {
    return false;
  }
}

/** Template placeholders like {{secret.NAME}} are allowed in URLs. */
export function isUrlOrTemplate(v: string): boolean {
  const text = v.trim();
  // A secret may contain a complete URL; its scheme can only be checked at execution.
  if (/^\{\{\s*secret\.[A-Z][A-Z0-9_]*\s*\}\}/.test(text)) return true;
  return isHttpUrl(text.replace(/\{\{[^{}]+\}\}/g, "template"));
}

export function utf8Length(v: string): number {
  return new TextEncoder().encode(v).length;
}

export function validateSecretName(name: string): string | null {
  if (!name) return "Name is required";
  if (!isSecretName(name)) return "Use UPPER_SNAKE_CASE: start with a letter, then letters, digits or underscores, up to 64 characters";
  return null;
}

export function validateSecretValue(value: string): string | null {
  if (value === "") return "Value is required";
  if (utf8Length(value) > rules.secretValueMaxBytes) return `Value must be at most ${rules.secretValueMaxBytes} bytes`;
  return null;
}

export function validateKeyName(name: string): string | null {
  const n = name.trim();
  if (n.length < 1) return "Name is required";
  if (n.length > rules.keyNameMax) return `Name must be at most ${rules.keyNameMax} characters`;
  return null;
}

export function validateWorkspace(name: string, slug: string): { name?: string; slug?: string } {
  const out: { name?: string; slug?: string } = {};
  if (name.trim().length < 1) out.name = "Name is required";
  else if (name.trim().length > rules.workspaceNameMax) out.name = `Name must be at most ${rules.workspaceNameMax} characters`;
  if (!isWorkspaceSlug(slug)) out.slug = "Use lowercase letters, digits, - or _ (up to 64 characters, starting with a letter or digit)";
  return out;
}

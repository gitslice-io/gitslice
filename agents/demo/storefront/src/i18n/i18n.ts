export type Messages = Record<string, string>;

// t looks a key up in the locale's messages and falls back to English.
export function t(messages: Messages, fallback: Messages, key: string, vars: Record<string, string | number> = {}): string {
  const template = messages[key] ?? fallback[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (_, name: string) => String(vars[name] ?? `{${name}}`));
}

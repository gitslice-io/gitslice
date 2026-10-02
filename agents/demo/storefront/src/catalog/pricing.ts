import type { Product } from "./types";

const SYMBOLS: Record<Product["currency"], string> = { USD: "$", EUR: "€", GBP: "£", JPY: "¥" };

export function formatPrice(cents: number, currency: Product["currency"]): string {
  if (currency === "JPY") return `${SYMBOLS.JPY}${Math.round(cents / 100)}`;
  return `${SYMBOLS[currency]}${(cents / 100).toFixed(2)}`;
}

export function applyDiscount(cents: number, percent: number): number {
  if (percent < 0 || percent > 100) throw new Error("discount must be between 0 and 100");
  return Math.round(cents * (1 - percent / 100));
}

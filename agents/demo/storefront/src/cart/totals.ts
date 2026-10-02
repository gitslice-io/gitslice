import type { CartLine } from "./cart";

export const TAX_RATE = 0.08;

export function subtotal(lines: CartLine[]): number {
  let total = 0;
  for (const line of lines) {
    total += line.unitCents * line.quantity;
  }
  return total;
}

export function tax(subtotalCents: number): number {
  return Math.round(subtotalCents * TAX_RATE);
}

export function shipping(subtotalCents: number): number {
  if (subtotalCents >= 5000) return 0;
  return 599;
}

export function total(lines: CartLine[]): number {
  const sub = subtotal(lines);
  return sub + tax(sub) + shipping(sub);
}

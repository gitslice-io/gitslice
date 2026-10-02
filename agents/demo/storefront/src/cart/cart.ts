import type { Product } from "../catalog/types";

export interface CartLine {
  sku: string;
  quantity: number;
  unitCents: number;
}

export class Cart {
  private lines = new Map<string, CartLine>();

  add(product: Product, quantity = 1): void {
    if (quantity <= 0) throw new Error("quantity must be positive");
    const line = this.lines.get(product.sku);
    if (line) line.quantity += quantity;
    else this.lines.set(product.sku, { sku: product.sku, quantity, unitCents: product.priceCents });
  }

  remove(sku: string): void {
    this.lines.delete(sku);
  }

  items(): CartLine[] {
    return [...this.lines.values()];
  }

  isEmpty(): boolean {
    return this.lines.size === 0;
  }
}

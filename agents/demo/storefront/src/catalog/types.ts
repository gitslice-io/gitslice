export interface Product {
  sku: string;
  name: string;
  priceCents: number;
  currency: "USD" | "EUR" | "GBP" | "JPY";
  tags: string[];
  stock: number;
}

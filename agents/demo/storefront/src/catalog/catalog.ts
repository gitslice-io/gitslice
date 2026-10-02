import type { Product } from "./types";

// Products live in src/catalog/products/<sku>.json, one file per product.
export function loadCatalog(entries: Product[]): Map<string, Product> {
  const catalog = new Map<string, Product>();
  for (const product of entries) {
    if (catalog.has(product.sku)) throw new Error(`duplicate sku ${product.sku}`);
    catalog.set(product.sku, product);
  }
  return catalog;
}

export function inStock(catalog: Map<string, Product>): Product[] {
  return [...catalog.values()].filter((p) => p.stock > 0);
}

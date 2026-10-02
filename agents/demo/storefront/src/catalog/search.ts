import type { Product } from "./types";

export function search(products: Product[], query: string): Product[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  return products.filter((p) => {
    const haystack = `${p.name} ${p.tags.join(" ")}`.toLowerCase();
    return terms.every((t) => haystack.includes(t));
  });
}

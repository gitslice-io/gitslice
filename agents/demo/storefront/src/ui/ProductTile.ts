export interface ProductTileProps {
  name: string;
}

export function ProductTile(props: ProductTileProps): string {
  return `<article class="product-tile">${props.name}</article>`;
}

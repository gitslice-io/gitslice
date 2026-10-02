export interface ProductGalleryProps {
  title: string;
}

export function ProductGallery(props: ProductGalleryProps): string {
  return `<section class="gallery">${props.title}</section>`;
}

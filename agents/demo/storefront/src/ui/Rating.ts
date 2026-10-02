export interface RatingProps {
  stars: number;
}

export function Rating(props: RatingProps): string {
  return `<div class="rating">${'★'.repeat(props.stars)}</div>`;
}

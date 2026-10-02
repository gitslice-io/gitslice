export interface CardProps {
  title: string;
}

export function Card(props: CardProps): string {
  return `<div class="card">${props.title}</div>`;
}

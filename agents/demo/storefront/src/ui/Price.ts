export interface PriceProps {
  text: string;
}

export function Price(props: PriceProps): string {
  return `<span class="price">${props.text}</span>`;
}

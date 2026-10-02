export interface BadgeProps {
  text: string;
}

export function Badge(props: BadgeProps): string {
  return `<span class="badge">${props.text}</span>`;
}

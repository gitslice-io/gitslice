export interface IconButtonProps {
  icon: string;
}

export function IconButton(props: IconButtonProps): string {
  return `<button class="icon-btn">${props.icon}</button>`;
}

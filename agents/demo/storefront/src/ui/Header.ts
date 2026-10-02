export interface HeaderProps {
  title: string;
}

export function Header(props: HeaderProps): string {
  return `<header class="site-header">${props.title}</header>`;
}

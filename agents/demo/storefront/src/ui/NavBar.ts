export interface NavBarProps {
  links: string[];
}

export function NavBar(props: NavBarProps): string {
  return `<nav class="nav">${props.links.join(' · ')}</nav>`;
}

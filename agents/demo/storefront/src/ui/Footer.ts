export interface FooterProps {
  text: string;
}

export function Footer(props: FooterProps): string {
  return `<footer class="site-footer">${props.text}</footer>`;
}

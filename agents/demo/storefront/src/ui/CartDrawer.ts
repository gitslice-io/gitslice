export interface CartDrawerProps {
  title: string;
}

export function CartDrawer(props: CartDrawerProps): string {
  return `<aside class="cart-drawer">${props.title}</aside>`;
}

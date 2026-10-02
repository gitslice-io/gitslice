export interface CartLineItemProps {
  name: string;
}

export function CartLineItem(props: CartLineItemProps): string {
  return `<li class="cart-line">${props.name}</li>`;
}

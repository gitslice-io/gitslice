export interface CheckoutFormProps {
  title: string;
}

export function CheckoutForm(props: CheckoutFormProps): string {
  return `<form class="checkout-form">${props.title}</form>`;
}

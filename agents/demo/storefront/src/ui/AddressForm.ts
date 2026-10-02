export interface AddressFormProps {
  title: string;
}

export function AddressForm(props: AddressFormProps): string {
  return `<form class="address-form">${props.title}</form>`;
}

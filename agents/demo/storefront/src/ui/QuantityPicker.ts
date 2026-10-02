export interface QuantityPickerProps {
  quantity: number;
}

export function QuantityPicker(props: QuantityPickerProps): string {
  return `<div class="qty">${String(props.quantity)}</div>`;
}

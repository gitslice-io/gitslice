export interface ToastProps {
  message: string;
}

export function Toast(props: ToastProps): string {
  return `<div class="toast">${props.message}</div>`;
}

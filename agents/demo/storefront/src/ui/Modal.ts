export interface ModalProps {
  title: string;
}

export function Modal(props: ModalProps): string {
  return `<div class="modal">${props.title}</div>`;
}

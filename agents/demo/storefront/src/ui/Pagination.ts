export interface PaginationProps {
  page: number; pages: number;
}

export function Pagination(props: PaginationProps): string {
  return `<nav class="pagination">${`${props.page} / ${props.pages}`}</nav>`;
}

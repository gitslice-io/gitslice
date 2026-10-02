export interface SearchBoxProps {
  placeholder: string;
}

export function SearchBox(props: SearchBoxProps): string {
  return `<form class="search">${props.placeholder}</form>`;
}

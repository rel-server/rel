import { css } from "elt"

export const dialog = css`.dialog {
  --width: min(75%, 860px);
  height: min(90%, 860px);
}`

export const error = css`.error {
  width: 100%;
  overrflow: auto;
}`

export const clickable = css`.clickable {
  cursor: pointer;
  transition: outline var(--sl-transition-duration-x-fast) ease-in-out;
  outline: none;

  &:hover {
    background: rgba(0, 0, 0, 0.05);
  }

  &:focus-visible {
    outline-offset: var(--sl-focus-ring-offset);

    outline: 2px solid var(--sl-color-primary-500) !important;
  }
}`
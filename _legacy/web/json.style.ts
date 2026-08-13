import { css } from "elt"

export const string = css`.string {
  font-family: ui-monospace, monospace;
  color:rgb(5, 142, 122);
}`

export const property = css`
.property {
  font-family: ui-monospace, monospace;
  color: #a71d5d;
}
`

export const constant = css`
.constant {
  font-family: ui-monospace, monospace;
  color: #0086b3;
}
`

export const punctuation = css`
.punctuation {
  font-family: ui-monospace, monospace;
  position: relative;
  color: #999;
}
`

export const number = css`
.number {
  font-family: ui-monospace, monospace;
  color:rgb(210, 101, 38);
}
`

export const container = css`.container {
  margin-left: .5em;
  margin-top: .25em;
  margin-bottom: .25em;
  padding-left: .5em;
  border-left: 1px solid rgba(0, 0, 0, 0.1);
  position: relative;
  white-space: pre-wrap;
  font-family: ui-monospace, monospace;
  border-radius: 0.5em;
}`

export const clickable = css`.clickable {
  cursor: pointer;
}`

export const copied = css`.copied {
  position: absolute;
  top: 0;
  left: 0;
  z-index: 1;
  width: 150px;
  text-align: center;
  background-color: #000;
  color: #fff;
  padding: 0.5em;
  border-radius: 0.5em;
}`
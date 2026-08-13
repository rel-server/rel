#!/usr/bin/env bun
/// <reference types="./web/node_modules/@types/bun/index.d.ts" />
/**
 * patron — templating language that compiles `.pat` files to plain Go.
 *
 * Port of the go-patron package. The lexer tokenizes `@`-directives, applies
 * whitespace/indent rules, then emits Go that writes to an `io.Writer` or
 * returns a `string` (same algorithm as the Go implementation).
 *
 * Usage:  bun patron.ts <file.pat> [more.pat ...]
 * Output: sibling `.go` file per input, package name = parent directory name.
 */

import { basename, dirname, extname, resolve } from "node:path";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Token types
// ---------------------------------------------------------------------------

const enum TokenType {
  Text = 0,
  Space,
  Newline,
  Code, // @{ ... }
  OutputCode, // @(expr) or @identifier
  Control, // @func, @if, @for, ...
  Statement, // @break, @continue, @return
  End, // }
  Illegal,
  String, // @"..." etc.
  Slice, // ...{prefix}variable{suffix}|{separator}
}

const tokenTypeNames: Record<TokenType, string> = {
  [TokenType.Text]: "Text",
  [TokenType.Space]: "Space",
  [TokenType.Newline]: "Newline",
  [TokenType.Code]: "Code",
  [TokenType.OutputCode]: "OutputCode",
  [TokenType.Control]: "Control",
  [TokenType.Statement]: "Statement",
  [TokenType.End]: "End",
  [TokenType.Illegal]: "Illegal",
  [TokenType.String]: "String",
  [TokenType.Slice]: "Slice",
};

function debugTokenType(t: TokenType): string {
  return tokenTypeNames[t];
}

// ---------------------------------------------------------------------------
// Token
// ---------------------------------------------------------------------------

interface Token {
  type: TokenType;
  // lexer: Lexer;
  /** Token source slice as a string (Go: []rune). */
  content: string;
  pos: number;
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
  /** fmt.Sprintf verb suffix, e.g. "%d" or "%.1f". */
  fmt: string;
  skip: boolean;
  fixIndent: number;

  // this is weird and a bend of what a parser should be, but it's convenient this way
  prefix?: Token[];
  suffix?: Token[];
  separator?: Token[];
  slice_expr?: Token[];
}

function isTextToken(t: Token): boolean {
  return (
    t.type === TokenType.Text ||
    t.type === TokenType.Space ||
    t.type === TokenType.Newline
  );
}

function isLetter(c: string): boolean {
  return /\p{L}/u.test(c);
}

function isDigit(c: string): boolean {
  return /\p{N}/u.test(c);
}

/** Horizontal / non-newline whitespace (main lexer space tokens). */
function isSpace(c: string): boolean {
  return c !== "\n" && /\s/u.test(c);
}

/** Any Unicode whitespace, including newline — matches Go unicode.IsSpace. */
function isUnicodeSpace(c: string): boolean {
  return /\s/u.test(c);
}

/** Split input into Unicode code points (Go runes). */
function toRunes(s: string): string[] {
  return Array.from(s);
}

function runesToString(runes: string[]): string {
  return runes.join("");
}

// ---------------------------------------------------------------------------
// Dedent
// ---------------------------------------------------------------------------

/**
 * Detect indentation from the first non-space after a newline and remove that
 * many leading spaces from every line. If the first non-space appears before
 * any newline, trim outer whitespace only.
 */
function dedent(runes: string[]): string[] {
  let firstNonSpaceIdx = -1;
  let spaceCount = 0;
  let lastNewlineIdx = -1;

  for (let i = 0; i < runes.length; i++) {
    const r = runes[i];
    if (r === "\n") {
      lastNewlineIdx = i;
      spaceCount = 0;
      continue;
    }
    if (isSpace(r)) {
      spaceCount++;
      continue;
    }
    firstNonSpaceIdx = i;
    break;
  }

  if (firstNonSpaceIdx === -1) {
    return runes;
  }

  if (lastNewlineIdx === -1 || firstNonSpaceIdx < lastNewlineIdx) {
    return toRunes(runesToString(runes).trim());
  }

  const indent = spaceCount;
  if (indent === 0) {
    return runes;
  }

  const out: string[] = [];
  let start = 0;
  for (let i = 0; i <= runes.length; i++) {
    if (i === runes.length || runes[i] === "\n") {
      const line = runes.slice(start, i);
      let trim = 0;
      let j = 0;
      for (; j < line.length && trim < indent; j++) {
        if (line[j] === " ") {
          trim++;
        } else {
          break;
        }
      }
      out.push(...line.slice(j));
      if (i < runes.length) {
        out.push("\n");
      }
      start = i + 1;
    }
  }

  return out;
}

class Lexer {
  input: string;
  pos = 0;
  line = 1;
  column = 1;
  tokens: Token[] = [];

  constructor(input: string) {
    this.input = input;
    this.lex(false);
    this.collapseSpaces();
  }

  /** Main scan loop — same structure as Go Lex(). */
  lex(waitingForBracket: boolean, discardEndBracket = false): void {
    while (this.pos < this.input.length) {
      const c = this.input[this.pos];

      if (c === "\n") {
        this.newToken(TokenType.Newline, this.pos, this.pos + 1);
      } else if (isSpace(c)) {
        const start = this.pos;
        this.pos++;
        while (this.pos < this.input.length && isSpace(this.input[this.pos])) {
          this.pos++;
        }
        this.newToken(TokenType.Space, start, this.pos);
      } else if (c === "@" && this.pos + 1 < this.input.length) {
        this.lexAt();
      } else if (waitingForBracket && c === "}") {
        if (!discardEndBracket) {
          this.newToken(TokenType.End, this.pos, this.pos + 1);
        } else {
          this.pos++
        }
        return;
      } else {
        this.lexText(waitingForBracket);
      }
    }
  }

  /** Plain text until `@`, whitespace, or closing `}` when inside a block. */
  lexText(waitingForBracket: boolean): void {
    const start = this.pos;
    let pos = this.pos;

    while (pos < this.input.length) {
      const ch = this.input[pos];
      if (
        isUnicodeSpace(ch) ||
        ch === "@" ||
        (waitingForBracket && ch === "}")
      ) {
        break;
      }
      pos++;
    }

    this.newToken(TokenType.Text, start, pos);
  }

  steal(fn: () => void): Token[] {
    let token_pos = this.tokens.length;
    fn();
    const tokens = this.tokens.slice(token_pos, this.tokens.length);
    this.tokens = this.tokens.slice(0, token_pos);
    return tokens;
  }

  /** Handle `@` directives — code blocks, control flow, output, strings. */
  lexAt(): void {
    const start = this.pos + 1; // skip leading @
    let pos = start;
    const c = this.input[pos];

    if (c === "@") {
      this.newToken(TokenType.Text, start, start + 1);
      return;
    }

    // ...{prefix}variable{suffix}|{separator}
    if (c === "." && this.input[pos + 1] === "." && this.input[pos + 2] === ".") {
      const tok = this.newToken(TokenType.Slice, this.pos, this.pos + 3);
      this.pos += 1;
      // We have a prefix
      let c2 = this.input[this.pos];
      // console.error("c2", c2, this.input.slice(this.pos, this.pos + 10))
      if (c2 === "{" || c2 === "`" || c2 === "\"") {
        const prefix = this.steal(() => {
          if (c2 === "{") {
            this.pos++
            this.lex(true, true)
          } else {
            this.pos--
            // will yield a string token
            this.lexAt()
          }
        })
        tok.prefix = prefix;
      }

      this.pos--
      // Now, we expect the expression
      // console.error("?", this.pos, this.input.slice(this.pos, this.pos + 100))
      const expr = this.steal(() => {
        this.lexAt()
      })
      tok.slice_expr = expr;

      let c3 = this.input[this.pos];
      // console.error("c3", c3, this.input.slice(this.pos, this.pos + 10))
      if (c3 === "{" || c3 === "`" || c3 === "\"") {
        const suffix = this.steal(() => {
          if (c3 === "{") {
            this.pos++
            this.lex(true, true)
          } else {
            this.pos--
            // will yield a string token
            this.lexAt()
          }
        })
        tok.suffix = suffix;
      }

      // console.error("separator", this.input[this.pos])
      if (this.input[this.pos] === "|") {
        this.pos++;
        let c4 = this.input[this.pos];
        // console.error("c4", c4, this.input.slice(this.pos, this.pos + 10))

        const separator = this.steal(() => {
          if (c4 === "{") {
            this.pos++
            this.lex(true, true)
          } else {
            this.pos--
            // will yield a string token
            this.lexAt()
          }
        })
        tok.separator = separator;
      }
      return;
    }

    if (c === "(" || c === "{") {
      const startIsParen = c === "(";
      const kind = startIsParen ? TokenType.OutputCode : TokenType.Code;
      const until = startIsParen ? ")" : "}";

      pos = this.advanceGoCodeUntil(pos, until);
      const tk = this.newToken(kind, start, pos);
      const inner = tk.content.slice(1, -1);
      tk.content = runesToString(dedent(toRunes(inner)));

      if (kind === TokenType.OutputCode) {
        this.parseFmtSpecifier(tk);
      }
      return;
    }

    if (c === "_" || isLetter(c)) {
      pos++;
      while (pos < this.input.length) {
        const ch = this.input[pos];
        if (!isLetter(ch) && !isDigit(ch) && ch !== "_") {
          break;
        }
        pos++;
      }

      const identifier = this.input.slice(start, pos);

      switch (identifier) {
        case "func":
        case "if":
        case "else":
        case "elseif":
        case "for":
        case "switch":
        case "case":
        case "default": {
          pos = this.advanceGoCodeUntil(pos, "{");
          this.newToken(TokenType.Control, start, pos);
          this.lex(true);
          return;
        }
        case "break":
        case "continue":
        case "return":
          this.newToken(TokenType.Statement, start, pos);
          return;
        default: {
          while (pos < this.input.length) {
            const ch = this.input[pos];
            if (ch === ".") {
              pos++;
              let found = false;
              while (
                pos < this.input.length &&
                (isLetter(this.input[pos]) ||
                  isDigit(this.input[pos]) ||
                  this.input[pos] === "_")
              ) {
                found = true;
                pos++;
              }
              if (!found) {
                pos--;
                break;
              }
            } else if (ch === "(") {
              pos = this.advanceGoCodeUntil(pos, ")");
            } else if (ch === "[") {
              pos = this.advanceGoCodeUntil(pos, "]");
            } else {
              break;
            }
          }
          const tk = this.newToken(TokenType.OutputCode, start, pos);
          this.parseFmtSpecifier(tk);
          return;
        }
      }
    }

    if (c === '"' || c === "'" || c === "`") {
      pos++;
      while (pos < this.input.length) {
        if (this.input[pos] === "\\" && pos + 1 < this.input.length) {
          pos++;
        } else if (this.input[pos] === c) {
          pos++;
          break;
        }
        pos++;
      }
      this.newToken(TokenType.String, start, pos);
      return;
    }

    this.newToken(TokenType.Illegal, start, pos + 1);
  }

  /**
   * Advance through nested brackets/quotes/comments until `until` at depth 0.
   */
  advanceGoCodeUntil(pos: number, until: string): number {
    let balance = 0;
    let inQuote = " ";

    while (pos < this.input.length) {
      const c = this.input[pos];

      if (c === until && balance === 0) {
        pos++;
        break;
      }

      if (c === inQuote) {
        inQuote = " ";
      } else if (c === "(" || c === "{" || c === "[") {
        balance++;
      } else if (c === ")" || c === "}" || c === "]") {
        balance--;
      } else if (c === '"' || c === "'" || c === "`") {
        inQuote = c;
      } else if (c === "\\" && pos + 1 < this.input.length) {
        pos++;
      } else if (c === "/") {
        if (pos + 1 < this.input.length && this.input[pos + 1] === "/") {
          while (pos < this.input.length && this.input[pos] !== "\n") {
            pos++;
          }
        } else if (pos + 1 < this.input.length && this.input[pos + 1] === "*") {
          while (pos < this.input.length) {
            if (
              this.input[pos] === "*" &&
              pos + 1 < this.input.length &&
              this.input[pos + 1] === "/"
            ) {
              pos++;
              break;
            }
            pos++;
          }
        }
      }

      if (c === until && balance === 0) {
        pos++;
        break;
      }

      pos++;
    }

    return pos;
  }

  /** Optional `%` / `%.1f` suffix after an output expression. */
  parseFmtSpecifier(tk: Token): void {
    const start = this.pos;
    let pos = this.pos;
    if (pos >= this.input.length || this.input[pos] !== "%") {
      return;
    }
    pos++;
    if (pos < this.input.length && this.input[pos] === "#") {
      pos++;
    }
    let dotFound = false;
    while (pos < this.input.length) {
      if (!dotFound && this.input[pos] === ".") {
        dotFound = true;
      } else if (!isDigit(this.input[pos])) {
        break;
      }
      pos++;
    }

    if (pos >= this.input.length) {
      return;
    }

    pos++;
    tk.fmt = this.input.slice(start, pos);
    this.pos = pos;
  }

  /** Create a token and advance lexer position to `end`. */
  newToken(t: TokenType, start: number, end: number): Token {
    const startLine = this.line;
    const startColumn = this.column;

    for (let i = start; i < end; i++) {
      if (this.input[i] === "\n") {
        this.line++;
        this.column = 1;
      } else {
        this.column++;
      }
    }

    const tk: Token = {
      type: t,
      // lexer: this,
      content: this.input.slice(start, end),
      pos: start,
      startLine,
      startColumn,
      endLine: this.line,
      endColumn: this.column,
      fmt: "",
      skip: false,
      fixIndent: 0,
    };

    this.tokens.push(tk);
    this.pos = end;
    return tk;
  }

  /**
   * Whitespace control pass
   * Marks tokens to skip and sets FixIndent for dedented template text.
   */
  collapseSpaces(): number {
    let prev: Token | null = null;
    let tk: Token | null = null;
    let currentIndent = 0;
    const indentStack: number[] = [];
    let i = 0;

    while (i < this.tokens.length) {
      prev = tk;
      tk = this.tokens[i];

      switch (tk.type) {
        case TokenType.Space:
          if (prev?.type === TokenType.Newline && currentIndent > 0) {
            tk.fixIndent = currentIndent;
          }
          break;

        case TokenType.Control: {
          indentStack.push(currentIndent);

          let newlineEaten = false;
          let newlinePos = -1;

          for (
            let j = i + 1;
            j < this.tokens.length &&
            (this.tokens[j].type === TokenType.Space ||
              this.tokens[j].type === TokenType.Newline);
            j++
          ) {
            this.tokens[j].skip = true;
            if (this.tokens[j].type === TokenType.Newline) {
              newlineEaten = true;
              newlinePos = j;
              break;
            }
          }

          if (newlineEaten) {
            let ownIndent = 0;
            for (let j = i - 1; j >= 0; j--) {
              const t = this.tokens[j];
              if (t.type === TokenType.Space) {
                ownIndent = t.content.length;
              } else if (t.type === TokenType.Newline) {
                break;
              }
            }

            if (prev?.type === TokenType.Space) {
              prev.skip = true;
            }

            let nextIndent = -1;
            for (let j = newlinePos + 1; j < this.tokens.length; j++) {
              const t = this.tokens[j];
              if (
                t.type === TokenType.Space &&
                this.tokens[j - 1].type === TokenType.Newline
              ) {
                if (nextIndent === -1) {
                  nextIndent = t.content.length;
                } else {
                  nextIndent = Math.min(t.content.length, nextIndent);
                }
              } else if (
                t.type !== TokenType.Newline &&
                t.type !== TokenType.Space
              ) {
                break;
              }
            }

            currentIndent =
              nextIndent - Math.max(ownIndent - currentIndent, 0);
          }
          break;
        }

        case TokenType.End:
        case TokenType.Code: {
          if (tk.type === TokenType.End && indentStack.length > 0) {
            currentIndent = indentStack.pop()!;
          }

          if (prev?.type === TokenType.Space) {
            prev.skip = true;
          }

          let isAlone = true;
          for (let j = i - 1; j >= 0; j--) {
            const t = this.tokens[j];
            if (t.type === TokenType.Newline) {
              break;
            }
            if (t.type !== TokenType.Space && t.type !== TokenType.End) {
              isAlone = false;
              break;
            }
          }

          for (
            let j = i + 1;
            j < this.tokens.length &&
            (this.tokens[j].type === TokenType.Space ||
              (isAlone && this.tokens[j].type === TokenType.Newline));
            j++
          ) {
            this.tokens[j].skip = true;
            if (this.tokens[j].type === TokenType.Newline) {
              break;
            }
          }
          break;
        }
      }

      i++;
    }

    return i;
  }
}

// ---------------------------------------------------------------------------
// Code generation
// ---------------------------------------------------------------------------

const id = String.raw`\p{L}[\p{L}\p{N}_]*`;
const reFunc = new RegExp(
  `^func\\s+(${id})\\s*\\((${id})?\\s*([\\w.]*).*?\\)\\s*(string)?\\s*\\{`,
  "u",
);

/**
 * Walk tokens and write Go source. Tracks writer variable (`w` vs `ø` buffer),
 * indentation, and whether the enclosing function returns string.
 */
function generateGoCode(tokens: Token[]): string {
  const parts: string[] = [];

  const indentStack: string[] = [];
  let indent = "";

  let writerVar = "ø";
  let sepidx = 0
  let outputString = false;
  let balance = -1;

  const write = (s: string) => parts.push(s);

  function do_tokens(tokens: Token[]) {
    let i = 0;
    while (i < tokens.length) {
      const token = tokens[i];
      const content = token.content;

      switch (token.type) {
        case TokenType.Text:
        case TokenType.Space:
        case TokenType.Newline: {
          let str = "";
          while (i < tokens.length) {
            const tk = tokens[i];
            if (!isTextToken(tk)) {
              break;
            }
            if (!tk.skip) {
              if (tk.fixIndent > 0) {
                const slice = tk.content.slice(
                  Math.min(tk.fixIndent, tk.content.length),
                );
                str += slice;
              } else {
                str += tk.content;
              }
            }
            i++;
          }

          if (balance >= 0 && str !== "") {
            str = str.replaceAll("\\", "\\\\");
            str = str.replaceAll('"', '\\"');
            str = str.replaceAll("\n", "\\n");
            write(`${indent}${writerVar}.Write([]byte("${str}"))\n`);
          }
          continue;
        }

        case TokenType.Code:
          write(`${indent}${content}\n`);
          break;

        case TokenType.OutputCode:
          if (token.fmt !== "") {
            write(
              `${indent}${writerVar}.Write([]byte(fmt.Sprintf("${token.fmt}", ${content})))\n`,
            );
          } else {
            write(`${indent}${writerVar}.Write([]byte(${content}))\n`);
          }
          break;

        case TokenType.Slice: {
          let varname = "øiter" + sepidx++;
          let sep = "øsep" + sepidx++;

          if (token.separator) {
            write(`${indent}${sep} := false\n`)
          }

          write(`${indent}for _, ${varname} := range ${token.slice_expr![0]!.content} {\n`)
          if (token.separator) {
            write(`${indent}  if ${sep} {\n`)
            if (token.separator) {
              indentStack.push(indent);
              indent += "    ";
              do_tokens(token.separator);
              indent = indentStack.pop()!;
            }
            write(`${indent}  } else {\n`)
            write(`${indent}    ${sep} = true\n`)
            write(`${indent}  }\n`)
          }

          if (token.prefix) {
            indentStack.push(indent);
            indent += "  ";
            do_tokens(token.prefix);
            indent = indentStack.pop()!;
          }

          write(`${indent}  ${writerVar}.Write([]byte(${varname}))\n`)

          if (token.suffix) {
            indentStack.push(indent);
            indent += "  ";
            do_tokens(token.suffix);
            indent = indentStack.pop()!;
          }

          write(`${indent}}\n`)


          // console.error("prefix", token.prefix)
          // console.error("expr", token.slice_expr)
          // console.error("suffix", token.suffix)
          // console.error("separator", token.separator)
          break;
        }

        case TokenType.Control: {
          balance++;
          const matches = content.match(reFunc);

          if (matches) {
            const writeVar = matches[2];
            const writeType = matches[3];
            const rettype = matches[4];

            if (writeType === "io.Writer") {
              writerVar = writeVar;
              outputString = false;
            } else if (rettype === "string") {
              writerVar = "ø";
              outputString = true;
            }
          }

          let ctrlContent = content;

          let separator: null | string = null;
          ctrlContent = ctrlContent.replace(/for\|("[^"]*")/, (match, rep) => {
            separator = rep;
            return "for"
          })

          if (!ctrlContent.startsWith("else")) {
            write(`\n${indent}`);
          } else {
            ctrlContent = ctrlContent.replace("elseif", "else if");
          }

          sepidx++
          if (separator != null) {
            write(`øsep${sepidx} := false\n${indent}`)
          }
          write(`${ctrlContent}\n`);

          indentStack.push(indent);
          indent += "  ";
          if (separator != null) {
            write(`${indent}if øsep${sepidx} {\n`)
            write(`${indent}  ${writerVar}.Write([]byte(${separator}))\n`)
            write(`${indent}} else {\n`)
            write(`${indent}  øsep${sepidx} = true\n`)
            write(`${indent}}\n`)
          }

          if (outputString && matches) {
            write(`${indent}var ${writerVar} bytes.Buffer\n`);
          }
          break;
        }

        case TokenType.End: {
          balance--;
          if (indentStack.length > 0) {
            indent = indentStack.pop()!;
          } else {
            indent = "";
          }

          if (indent === "" && outputString) {
            write(`${indent}  return ø.String()\n`);
          }
          write(`${indent}}`);

          if (i < tokens.length - 1) {
            let elsePosition = -1;
            for (let j = i + 1; j < tokens.length; j++) {
              const tk = tokens[j];
              if (
                tk.type === TokenType.Control &&
                tk.content.startsWith("else")
              ) {
                elsePosition = j;
                break;
              } else if (
                tk.type !== TokenType.Space &&
                tk.type !== TokenType.Newline
              ) {
                break;
              }
            }

            if (elsePosition === -1) {
              write("\n");
            } else {
              write(" ");
              i = elsePosition;
              continue;
            }
          }
          break;
        }

        case TokenType.Statement:
          write(`${indent}${content}\n`);
          break;

        case TokenType.String:
          write(`${indent}${writerVar}.Write([]byte(${content}))\n`);
          break;
      }

      i++;
    }
  }
  do_tokens(tokens);

  return parts.join("");
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

function compilePatFile(arg: string, fileIndex: number): void {
  const text = readFileSync(arg, "utf-8");
  const lexer = new Lexer(text);

  const outfilePath = arg.replace(extname(arg), ".go");
  const packageName = basename(dirname(resolve(arg)));

  const header = `package ${packageName}

import (
  "io"
  "fmt"
  "bytes"
  "testing"
)

`;

  const body = generateGoCode(lexer.tokens);
  const trailer = `

// just so that imports are not removed
func TestInclude${fileIndex}(t *testing.T) {
  var buf bytes.Buffer
  var st fmt.Stringer = &buf
  var w io.Writer = &buf
  _, _ = w.Write([]byte(st.String()))
}
`;

  writeFileSync(outfilePath, header + body + trailer, "utf-8");
  console.error(`Generated ${outfilePath}`);
}

// ---------------------------------------------------------------------------
// Exports (library use) + CLI entry when run directly
// ---------------------------------------------------------------------------

export {
  TokenType,
  debugTokenType,
  type Token,
  Lexer,
  dedent,
  generateGoCode,
  compilePatFile,
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  if (args.length === 0) {
    console.error("Usage: patron <path-to-pat-file> [...]");
    process.exit(1);
  }
  for (let i = 0; i < args.length; i++) {
    compilePatFile(args[i], i);
  }
}
